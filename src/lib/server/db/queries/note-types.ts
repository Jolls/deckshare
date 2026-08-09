/**
 * Note-type CRUD. Note types (`note_types`, `fields`, `templates`) are owned directly by a
 * user (`owner_id`) — they are not deck-scoped in the schema (`docs/schema.md`), so access
 * here is plain ownership, not the `deck_access` join `access.ts` does for decks. A note type
 * can be referenced by notes in any of the owner's decks.
 */
import { eq } from 'drizzle-orm';
import { noteTypes, fields, templates } from '../schema';
import { db } from '../index';
import { rethrowDuplicateName } from './errors';
import type { DbClient } from './types';

export interface FieldInput {
	name: string;
	font?: string;
	size?: number;
	isRtl?: boolean;
	sticky?: boolean;
}

export interface TemplateInput {
	name: string;
	qfmt: string;
	afmt: string;
	browserQfmt?: string;
	browserAfmt?: string;
}

export interface CreateNoteTypeInput {
	name: string;
	css?: string;
	isCloze?: boolean;
	sortFieldIdx?: number;
	fields: FieldInput[];
	templates: TemplateInput[];
}

/** Thrown by `requireNoteTypeOwner`. Mirrors `DeckAccessError`'s role in `access.ts`, but for
 *  the ownership check note types use instead of a `deck_access` join. */
export class NoteTypeAccessError extends Error {
	constructor(public readonly noteTypeId: string) {
		super(`note type ${noteTypeId}: not owned by caller`);
		this.name = 'NoteTypeAccessError';
	}
}

/** Throws `NoteTypeAccessError` if the note type doesn't exist or isn't owned by `userId`.
 *  Used anywhere a note type is *referenced* by a mutation on something else (e.g. attaching
 *  a note to it) rather than being the resource under CRUD itself. */
export async function requireNoteTypeOwner(client: DbClient, userId: string, noteTypeId: string) {
	const [noteType] = await client.select().from(noteTypes).where(eq(noteTypes.id, noteTypeId));
	if (!noteType || noteType.ownerId !== userId) throw new NoteTypeAccessError(noteTypeId);
	return noteType;
}

/** Creates a note type with its fields and templates in one transaction. Ordinals follow
 *  array order. Throws `DuplicateNameError` if the user already owns one of that name — the
 *  name is the key import dedups on, so it can't be reused. */
export async function createNoteType(
	userId: string,
	input: CreateNoteTypeInput,
	client: DbClient = db
) {
	return rethrowDuplicateName('note type', input.name, () =>
		client.transaction(async (tx) => {
			const [noteType] = await tx
				.insert(noteTypes)
				.values({
					ownerId: userId,
					name: input.name,
					css: input.css ?? '',
					isCloze: input.isCloze ?? false,
					sortFieldIdx: input.sortFieldIdx ?? 0
				})
				.returning();
			if (!noteType) throw new Error('note type insert returned no row');

			if (input.fields.length > 0) {
				await tx.insert(fields).values(
					input.fields.map((f, ordinal) => ({
						noteTypeId: noteType.id,
						ordinal,
						name: f.name,
						font: f.font ?? 'Arial',
						size: f.size ?? 20,
						isRtl: f.isRtl ?? false,
						sticky: f.sticky ?? false
					}))
				);
			}
			if (input.templates.length > 0) {
				await tx.insert(templates).values(
					input.templates.map((t, ordinal) => ({
						noteTypeId: noteType.id,
						ordinal,
						name: t.name,
						qfmt: t.qfmt,
						afmt: t.afmt,
						browserQfmt: t.browserQfmt,
						browserAfmt: t.browserAfmt
					}))
				);
			}

			return noteType;
		})
	);
}

export async function listNoteTypesForUser(userId: string, client: DbClient = db) {
	return client.select().from(noteTypes).where(eq(noteTypes.ownerId, userId));
}

async function fieldsAndTemplatesFor(noteTypeId: string, client: DbClient) {
	const [fieldRows, templateRows] = await Promise.all([
		client.select().from(fields).where(eq(fields.noteTypeId, noteTypeId)),
		client.select().from(templates).where(eq(templates.noteTypeId, noteTypeId))
	]);
	return { fields: fieldRows, templates: templateRows };
}

/** `null` if the note type doesn't exist or isn't owned by `userId`. */
export async function getNoteType(userId: string, noteTypeId: string, client: DbClient = db) {
	const [noteType] = await client.select().from(noteTypes).where(eq(noteTypes.id, noteTypeId));
	if (!noteType || noteType.ownerId !== userId) return null;
	const rest = await fieldsAndTemplatesFor(noteTypeId, client);
	return { ...noteType, ...rest };
}

export interface UpdateNoteTypeInput {
	name?: string;
	css?: string;
	isCloze?: boolean;
	sortFieldIdx?: number;
}

/** Updates the note type's own columns. Editing its fields/templates after creation is out
 *  of scope for now — every note type in Phase 1 is authored fresh or imported whole. */
export async function updateNoteType(
	userId: string,
	noteTypeId: string,
	patch: UpdateNoteTypeInput,
	client: DbClient = db
) {
	const [existing] = await client.select().from(noteTypes).where(eq(noteTypes.id, noteTypeId));
	if (!existing || existing.ownerId !== userId) return null;

	// A unique violation here can only be the rename colliding with another of the user's types.
	const [updated] = await rethrowDuplicateName('note type', patch.name ?? '', () =>
		client.update(noteTypes).set(patch).where(eq(noteTypes.id, noteTypeId)).returning()
	);
	return updated ?? null;
}

/** Fails at the database with a FK-restrict violation if any note still references this note
 *  type — that's intentional, not handled here (CLAUDE.md §3: surgical, no speculative
 *  error-handling for a case the schema already prevents). */
export async function deleteNoteType(userId: string, noteTypeId: string, client: DbClient = db) {
	const [existing] = await client.select().from(noteTypes).where(eq(noteTypes.id, noteTypeId));
	if (!existing || existing.ownerId !== userId) return false;

	await client.delete(noteTypes).where(eq(noteTypes.id, noteTypeId));
	return true;
}
