# Plan: Deck / note-type / note management UI (#29)

## Context confirmed from actual code

- **Auth**: `hooks.server.ts` populates `event.locals.user: SessionUser | null`
  (`{id, email, displayName}`) from the session cookie on every request. No
  `(app)/+layout.server.ts` guard exists in the current tree —
  `(app)/study/[deckId]/+page.server.ts` does its own
  `if (!locals.user) throw error(401, ...)`. #28 (auth UI, landing first in this batch)
  does not add a shared guard either. Every new page in this plan performs its own
  `locals.user` check — correct regardless of what #28 does, redundant at worst.
- **Query layer signatures** (all in `src/lib/server/db/queries/`):
  - `decks.ts`: `createDeck(userId, {name, description?, visibility?, preset?})`,
    `listDecksForUser(userId) -> {deck, role}[]`, `getDeck(userId, deckId) -> {deck, role}`
    (throws `DeckAccessError`), `updateDeck`, `deleteDeck` (requires `owner` role).
  - `note-types.ts`: `createNoteType(userId, {name, css?, isCloze?, sortFieldIdx?,
    fields: {name,font?,size?,isRtl?,sticky?}[], templates: {name,qfmt,afmt,
    browserQfmt?,browserAfmt?}[]})`, `listNoteTypesForUser(userId)`,
    `getNoteType(userId, noteTypeId) -> {...noteType, fields, templates} | null`.
  - `notes.ts`: `createNote(userId, deckId, {noteTypeId, guid, fields: string[], tags?})
    -> {note, cards}`, `listNotesForDeck(userId, deckId)`, `getNote`,
    `deleteNote(userId, deckId, noteId) -> boolean`. **`fields` is a plain array
    positionally matched to `fields.ordinal`** — no field names are stored on the note
    row itself; names live only on the note type's `fields` rows. Card fan-out
    (`cardRowsFor`) is fully internal to `createNote` — the UI supplies nothing beyond
    `fields: string[]`; no new query logic needed anywhere for this issue.
- **Schema**: `note_types` are **owned per-user** (`owner_id`), not deck-scoped — one
  note type can back notes in any of the owner's decks. `fields.ordinal` and
  `templates.ordinal` define array order.
- **API routes exist and work** as JSON CRUD under `src/routes/api/**` — **not used by
  this plan's pages**. Precedent (`study/[deckId]/+page.server.ts`) calls the query
  layer directly from `+page.server.ts`, bypassing the API entirely for server-rendered
  pages. This plan follows that precedent for every page: `load` functions and form
  `actions` import from `$lib/server/db/queries/*` directly. The `api/` routes remain for
  future client-side/JS-driven consumers (the write queue, a future SPA-style flow), not
  for this SSR CRUD UI.
- **Sanitisation**: `sanitiseCardHtml` runs only at render time
  (`src/lib/render/sanitise.ts`), consumed today only by `getReviewSession`. This UI
  writes raw field values into `notes.fields` (jsonb text) — exactly what the reviewer
  already sanitises before display. **No input-side sanitisation needed in this plan.**
- **No shared `(app)` layout/components exist** beyond the bare root `+layout.svelte`
  (favicon only), plus whatever `(app)/+layout.svelte` #28 adds (logout button, per
  `docs/plans/28-auth-ui.md`) — this plan's pages render inside that layout, adding no
  layout of their own. `src/lib/components/` is empty (`.gitkeep` only). No conventions
  to match beyond `study/[deckId]/+page.svelte` (Svelte 5 runes, scoped `<style>`, no
  component library).

## Resolved decision (user confirmed)

**Note-type resolution: hardcoded default "Basic" note type, auto-seeded lazily, one per
user.** No note-type creation form, no `(app)/note-types/` route. A minimal valid "Basic"
note type: fields `["Front", "Back"]`, one template
`{name: "Card 1", qfmt: "{{Front}}", afmt: "{{FrontSide}}<hr id=answer>{{Back}}"}`.

## Routes to add

### 1. `src/routes/(app)/decks/+page.server.ts` (new)

```ts
import { redirect } from '@sveltejs/kit';
import { fail } from '@sveltejs/kit';
import type { Actions, PageServerLoad } from './$types';
import { createDeck, listDecksForUser } from '$lib/server/db/queries/decks';

export const load: PageServerLoad = async ({ locals }) => {
	if (!locals.user) throw redirect(303, '/login');
	const decks = await listDecksForUser(locals.user.id);
	return { decks };
};

export const actions: Actions = {
	create: async ({ request, locals }) => {
		if (!locals.user) throw redirect(303, '/login');
		const form = await request.formData();
		const name = form.get('name');
		if (typeof name !== 'string' || !name.trim()) {
			return fail(400, { error: 'Deck name is required' });
		}
		const description = form.get('description');
		const deck = await createDeck(locals.user.id, {
			name: name.trim(),
			description: typeof description === 'string' && description.trim() ? description.trim() : undefined
		});
		throw redirect(303, `/decks/${deck.id}`);
	}
};
```

(`visibility`/`preset` omitted → `createDeck` defaults apply; no sharing UI, out of scope.)

### 2. `src/routes/(app)/decks/+page.svelte` (new)

- `let { data, form } = $props();`
- List `data.decks` (each: link to `/decks/${deck.id}`, showing `deck.name`).
- Empty state: "No decks yet." when `data.decks.length === 0`.
- `<form method="POST" action="?/create">`: `name` (required text input),
  `description` (optional text input), submit button. Show `form?.error` if present
  (from the `fail(400, ...)` case).

### 3. `src/routes/(app)/decks/_note-type-defaults.ts` (new, route-adjacent helper —
not a query module, no new query/access logic)

```ts
import { createNoteType, getNoteType, listNoteTypesForUser } from '$lib/server/db/queries/note-types';
import type { NoteTypeWithFieldsAndTemplates } from '$lib/server/db/queries/note-types';

const BASIC_NOTE_TYPE_INPUT = {
	name: 'Basic',
	fields: [{ name: 'Front' }, { name: 'Back' }],
	templates: [
		{ name: 'Card 1', qfmt: '{{Front}}', afmt: '{{FrontSide}}<hr id=answer>{{Back}}' }
	]
};

/** Returns the user's one note type, creating the default "Basic" type on first use. */
export async function resolveNoteType(userId: string): Promise<NoteTypeWithFieldsAndTemplates> {
	const existing = await listNoteTypesForUser(userId);
	if (existing.length === 0) {
		const created = await createNoteType(userId, BASIC_NOTE_TYPE_INPUT);
		const full = await getNoteType(userId, created.id);
		if (!full) throw new Error('note type vanished immediately after creation');
		return full;
	}
	const full = await getNoteType(userId, existing[0].id);
	if (!full) throw new Error('note type vanished between list and get');
	return full;
}
```

(Exact exported type name for `getNoteType`'s return — verify against
`src/lib/server/db/queries/note-types.ts` at implementation time; substitute the real
name if `NoteTypeWithFieldsAndTemplates` isn't it. The `if (!full)` guards are for
TypeScript's `noUncheckedIndexedAccess`/strict-null narrowing, not defensive
error-handling for a scenario expected to occur.)

### 4. `src/routes/(app)/decks/[deckId]/+page.server.ts` (new)

```ts
import { error, fail, redirect } from '@sveltejs/kit';
import type { Actions, PageServerLoad } from './$types';
import { DeckAccessError, getDeck } from '$lib/server/db/queries/decks';
import { deleteNote, listNotesForDeck } from '$lib/server/db/queries/notes';
import { resolveNoteType } from '../_note-type-defaults';

export const load: PageServerLoad = async ({ locals, params }) => {
	if (!locals.user) throw redirect(303, '/login');
	let deck;
	try {
		({ deck } = await getDeck(locals.user.id, params.deckId));
	} catch (err) {
		if (err instanceof DeckAccessError) throw error(err.status, err.message);
		throw err;
	}
	const notes = await listNotesForDeck(locals.user.id, params.deckId);
	const noteType = await resolveNoteType(locals.user.id);
	return { deck, notes, noteType };
};

export const actions: Actions = {
	deleteNote: async ({ request, locals, params }) => {
		if (!locals.user) throw redirect(303, '/login');
		const form = await request.formData();
		const noteId = form.get('noteId');
		if (typeof noteId !== 'string') return fail(400, { error: 'noteId is required' });
		await deleteNote(locals.user.id, params.deckId, noteId);
		return { success: true };
	}
};
```

(Verify `DeckAccessError`'s exact shape — constructor args / `.status` / `.message`
property names — against `src/lib/server/db/queries/decks.ts` and how
`study/[deckId]/+page.server.ts` or `api/_util.ts`'s `respondToAccessError` already
handles it, at implementation time; match that exact pattern instead of inventing a new
one.)

**Assumption**: since exactly one note type exists per user in this scope, all notes in
every one of that user's decks are displayed using that single note type's field names
for column headers. This holds as long as the hardcoded-default path (resolved above) is
what ships.

### 5. `src/routes/(app)/decks/[deckId]/+page.svelte` (new)

- Deck name/description header.
- Table: one column per `data.noteType.fields[i].name`, values from
  `note.fields[i]` for each `note` in `data.notes`, plus a per-row delete
  `<form method="POST" action="?/deleteNote">` with a hidden `noteId` input and a
  submit button.
- Empty state: "No notes yet." when `data.notes.length === 0`.
- Link: "Add note" → `/decks/${data.deck.id}/notes/new`.

### 6. `src/routes/(app)/decks/[deckId]/notes/new/+page.server.ts` (new)

```ts
import { error, redirect } from '@sveltejs/kit';
import type { Actions, PageServerLoad } from './$types';
import { DeckAccessError, getDeck } from '$lib/server/db/queries/decks';
import { createNote } from '$lib/server/db/queries/notes';
import { uuidv7 } from '$lib/uuid';
import { resolveNoteType } from '../../_note-type-defaults';

export const load: PageServerLoad = async ({ locals, params }) => {
	if (!locals.user) throw redirect(303, '/login');
	let deck;
	try {
		({ deck } = await getDeck(locals.user.id, params.deckId));
	} catch (err) {
		if (err instanceof DeckAccessError) throw error(err.status, err.message);
		throw err;
	}
	const noteType = await resolveNoteType(locals.user.id);
	return { deck, noteType };
};

export const actions: Actions = {
	create: async ({ request, locals, params }) => {
		if (!locals.user) throw redirect(303, '/login');
		const noteType = await resolveNoteType(locals.user.id);
		const form = await request.formData();
		const fields = noteType.fields
			.slice()
			.sort((a, b) => a.ordinal - b.ordinal)
			.map((field) => {
				const value = form.get(field.name);
				return typeof value === 'string' ? value : '';
			});
		const guid = uuidv7();
		await createNote(locals.user.id, params.deckId, { noteTypeId: noteType.id, guid, fields });
		throw redirect(303, `/decks/${params.deckId}`);
	}
};
```

(Verify `$lib/uuid`'s exact export name for the isomorphic UUIDv7 helper — CLAUDE.md §5
references it as `uuidv7()`; confirm against `src/lib/uuid.ts` or wherever it actually
lives at implementation time. Also verify `fields[i].ordinal` is the actual property name
on `note-types.ts`'s field row shape.)

No `tags` input — out of scope, omitted (defaults to `[]` per `createNote`'s signature).

### 7. `src/routes/(app)/decks/[deckId]/notes/new/+page.svelte` (new)

- One `<input>` per `data.noteType.fields` (sorted by `ordinal`), each labelled with
  `field.name`, and `name={field.name}` so `formData.get(field.name)` in the action
  matches.
- Submit button, cancel link back to `/decks/${data.deck.id}`.

## `CHANGELOG.md`

Same `## [Unreleased]` → `### Added` section #28 adds to (this batch lands both in one
PR) — append as an additional line, do not create a duplicate `## [Unreleased]` or
`### Added` header:

```
- Deck list/create, an auto-seeded default "Basic" note type, add-note form, and note list/delete pages under `src/routes/(app)/decks/` — the minimum UI to create study material without `curl` ([#29](https://github.com/Jolls/enshu/issues/29))
```

## Testing

No new test files. Justification: CLAUDE.md §10's priority list doesn't cover UI, and
rule 5 says skip tests for UI-only changes absent non-obvious edge cases. The query
functions this UI calls (`createDeck`, `createNoteType`, `createNote`, `deleteNote`,
`listDecksForUser`, `listNotesForDeck`, `getDeck`, `getNoteType`) are already covered by
existing tests in `src/lib/server/db/queries/*.test.ts` — this issue adds no new query
logic, only glue (`load`/`actions`) and markup. The one piece of actual logic this plan
introduces — `resolveNoteType`'s create-on-first-use branch and the ordinal-sorted
field-array mapping in the add-note action — is simple enough to verify by manual testing
(create first note as a fresh user, confirm a "Basic" type is seeded; add a second note,
confirm no duplicate type is created) rather than warranting a colocated test, per rule 5.

## Critical files for implementation

- `src/lib/server/db/queries/decks.ts`
- `src/lib/server/db/queries/note-types.ts`
- `src/lib/server/db/queries/notes.ts`
- `src/routes/(app)/study/[deckId]/+page.server.ts` (auth-guard and `DeckAccessError`
  handling precedent)
- `src/routes/api/_util.ts` (`respondToAccessError` — pattern reference only, not reused
  directly since this plan's pages throw SvelteKit `error()`, not JSON responses)
- `src/lib/uuid.ts` (or wherever the isomorphic `uuidv7()` helper lives — verify exact
  path/export)
</content>
