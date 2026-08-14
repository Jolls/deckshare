# HTTP routes

The planned route surface for `internal/http/` (architecture.md §4). **Nothing here is built** —
no Go code exists yet (architecture.md §1) — this is the target the scaffold session converges
on, not a description of running code. Update it in the same PR as any handler that adds,
renames, or removes a route.

Read [architecture.md §6](architecture.md#6-the-review-loop) first for the reviewer — the
contract for `POST /api/reviews/batch` is pinned down there in full and is not repeated here.

---

## Conventions

- **Pattern syntax is Go 1.22+ `net/http.ServeMux`**: `METHOD /path/{param}`. No router
  library (architecture.md §3: "no web framework required").
- **No client-side fetch except the reviewer's JS island** (§6). Every other route is a
  server-rendered page and a plain HTML form, and forms only speak GET/POST — so non-idempotent
  actions get their own POST path rather than a method override: `POST /x/{id}/edit`,
  `POST /x/{id}/delete`.
- **Auth**: every route requires a valid session unless marked **public**.
- **Permission** lists the `deck_access` flag(s) the query layer must check (CLAUDE.md §9) —
  never a handler-level guard alone. `deck_access` grants six independent booleans per
  `(user_id, deck_id)` — `can_view`, `can_study`, `can_edit_content`, `can_edit_settings`,
  `can_manage_access`, `can_delete` — not a role enum; see [schema.md](schema.md) for what each
  one grants. A route can require more than one flag. Blank means not deck-scoped:
  session-only, or gated by row ownership instead (e.g. a note type's `owner_id`).
- **Phase / step** cites architecture.md §11's build order, so route work can be sequenced
  against it.

---

## Auth — `auth.go` (Phase 1, step 3) -- built (#52)

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/signup` | public | Signup form |
| POST | `/signup` | public | Create account, `argon2id`-hash password, start session |
| GET | `/login` | public | Login form |
| POST | `/login` | public | Verify credentials, create session (cookie: hashed token, §12) |
| POST | `/logout` | session | Destroy session |
| GET | `/` | public | Authed: redirects to `/decks` (step 5, #54); redirects to `/login` otherwise |

---

## Decks — `decks.go` (Phase 1, step 5) -- built (#54)

| Method | Path | Permission | Purpose |
|---|---|---|---|
| GET | `/decks` | — | List decks reachable via `deck_access` |
| GET | `/decks/new` | — | New-deck form |
| POST | `/decks` | — | Create deck; creator gets a `deck_access` row with all six flags true |
| GET | `/decks/{id}` | `can_view` | Detail: notes list, note/card counts. Due counts deferred to step 7 (the reviewer) — they need `StudyDayStart`/`StudyDayEnd`, which don't exist yet, and there are no `user_card_state` rows at all before step 7 regardless |
| GET | `/decks/{id}/edit` | `can_edit_settings` | Edit form (name, description only — `preset` is not yet editable; its shape isn't read by any code until the reviewer's learning-steps config lands) |
| POST | `/decks/{id}/edit` | `can_edit_settings` | Update |
| POST | `/decks/{id}/delete` | `can_view`, `can_delete` | Delete deck, its cards, and any note left with no cards anywhere; notes with cards in other decks are re-homed. Query layer: `db.DeleteDeck` requires both flags — `can_view` is normally granted alongside every other flag by convention (schema.md), and requiring it here keeps a caller who somehow holds `can_delete` without `can_view` from learning the deck exists via a different error shape ([#51](https://github.com/Jolls/enshu/issues/51)); handler is Phase 1 step 5 |

---

## Note types — `notetypes.go` (Phase 1, step 5) -- built (#54)

Owner-scoped (`note_types.owner_id`), not deck-scoped — a note type is reusable across all of
its owner's decks.

| Method | Path | Access | Purpose |
|---|---|---|---|
| GET | `/note-types` | owns row | List the caller's own note types |
| GET | `/note-types/new` | — | New note-type form (fields + templates builder) |
| POST | `/note-types` | — | Create |
| GET | `/note-types/{id}/edit` | owns row | Edit form |
| POST | `/note-types/{id}/edit` | owns row | Update name/css/field-and-template renames, append new fields/templates. **Removing or reordering an existing field or template is refused with 409 while the note type has any notes** — `notes.fields` is a positional array, and a template removal would delete cards. Free while the note type has zero notes. Follow-up issue: field/template removal-and-reorder with positional note remap |
| POST | `/note-types/{id}/delete` | owns row | Delete — blocked while any note references it, enforced by `notes.note_type_id ON DELETE RESTRICT`; `fields` and `templates` cascade |

**Open question — not a route table decision, needs a call before Phase 2:** rendering a note
in a shared deck requires reading its note type's fields/templates, but `note_types` has no
`deck_access`-style row — only `owner_id`. A user with only `can_view`/`can_study` on a deck
whose notes use someone else's note type currently has no path to read it. Either note-type
read needs a second authorization path ("owns it, OR it backs a note in a deck I have access
to"), or note types need to be copied/forked into the sharing deck's own scope on first share.
Neither is decided.

---

## Notes — `notes.go` (Phase 1, step 5) -- built (#54)

Cards have **no routes of their own** — they're generated from a note × its note type's
templates (one per template, N per cloze ordinal — architecture.md §8) and destroyed/regenerated
as a side effect of note writes.

| Method | Path | Permission | Purpose |
|---|---|---|---|
| GET | `/decks/{deckId}/notes/new` | `can_edit_content` | New-note form (choose note type, fill fields) |
| POST | `/decks/{deckId}/notes` | `can_edit_content` | Create note; generates its cards |
| GET | `/notes/{id}/edit` | `can_edit_content` | Edit form |
| POST | `/notes/{id}/edit` | `can_edit_content` | Update fields/tags; regenerates cards if cloze ordinals changed |
| POST | `/notes/{id}/delete` | `can_edit_content` | Delete note and its cards — `cards.note_id ON DELETE CASCADE`, a single-statement delete; `review_log` rows for the deleted cards persist |
| POST | `/notes/{id}/move` | `can_edit_content` | Change `deck_id`; must also update denormalised `owner_id` (schema.md, "must not drift") |

The deck-detail notes list (`GET /decks/{id}`, above) is unpaginated — `ORDER BY modified_at
DESC LIMIT 200`. Follow-up issue: pagination once a deck's note count makes that limit visible.

---

## Review — `review.go` (Phase 1, step 7)

The core loop — full contract in architecture.md §6. Table here is deliberately thin; don't
duplicate the pseudocode.

| Method | Path | Permission | Purpose |
|---|---|---|---|
| GET | `/decks/{id}/review` | `can_view` + `can_study` | Reviewer page. First batch (20 cards), the precomputed 4-rating outcome per card, the user's FSRS params, and the study-day end are rendered inline in the response — no separate request for card 1 (§6) |
| GET | `/api/reviews/next` | `can_view` + `can_study` | Refill batch, JSON. Keyset cursor `(due, cardId)` + deck id; same per-card payload shape as the inline batch; server excludes cards already reviewed this study day (§6) |
| POST | `/api/reviews/batch` | `can_view` + `can_study` | Grade. `{events:[{id,cardId,rating,reviewedAt,durationMs}]}` — exactly these fields, idempotent, returns `<after>` per event. See §6 for the full authorise/recompute/store sequence and the concurrency mechanisms |

---

## Access — `access.go` (Phase 2)

Schema (`deck_access`) exists from Phase 1 step 2 even though nothing grants a second user
access until Phase 2 (architecture.md §11).

| Method | Path | Permission | Purpose |
|---|---|---|---|
| GET | `/decks/{id}/access` | `can_manage_access` | List collaborators and their permission flags |
| POST | `/decks/{id}/access` | `can_manage_access` | Grant access (email + choice of the six flags) |
| POST | `/decks/{id}/access/{userId}/edit` | `can_manage_access` | Change a collaborator's flags |
| POST | `/decks/{id}/access/{userId}/delete` | `can_manage_access` | Revoke access (delete the row) |

**Last-holder guard, enforced.** A deck must always retain at least one `can_manage_access`
holder and one `can_delete` holder. `db.RevokeDeckAccess` and `db.SetDeckAccess`
([#51](https://github.com/Jolls/enshu/issues/51)) apply the mutation and re-count holders in the
same transaction, under a deck row lock, returning `ErrLastAccessHolder` when it would strand the
deck — the handler answers **409**. Two consequences: a deck's sole member cannot revoke their
own access (they delete the deck instead), and `decks.owner_id` is not a permission source and
exempts nobody from the guard.

---

## Settings — `settings.go` (Phase 1: account settings step 3 -- built (#52), FSRS default step 9)

| Method | Path | Permission | Purpose |
|---|---|---|---|
| GET | `/settings` | session | Profile (display name, timezone, `day_start_hour`), password change, global FSRS default (`desired_retention` where `deck_id IS NULL`) |
| POST | `/settings` | session | Update profile |
| POST | `/settings/password` | session | Change password |
| POST | `/settings/fsrs` | session | Update the global `desired_retention` default |
| POST | `/decks/{id}/settings/fsrs` | `can_study` | Per-deck override. Scoped to the caller, not the deck — `user_fsrs_params` keys on `(user_id, deck_id)`, so this is "my retention target for this deck," not a deck-wide setting an admin sets for everyone |

`/settings/fsrs` and `/decks/{id}/settings/fsrs` are step 9 (`user_fsrs_params` isn't a UI concept
yet) and are not part of #52 — only the profile and password rows above are built.

---

## Import / export — `apkg.go` (Phase 1, step 8)

| Method | Path | Permission | Purpose |
|---|---|---|---|
| GET | `/import` | session | Upload form |
| POST | `/import` | session | Upload `.apkg`; synchronous `read → IR → db`, redirects to the resulting deck. Idempotent on `(owner_id, guid)` (§7, invariant §2.2) |
| GET | `/decks/{id}/export` | `can_view` | Stream a `.apkg` (`db → IR → write`, `Content-Disposition: attachment`) — a read of the deck's content, not an edit |

Synchronous upload is a Simplicity First choice for MVP — no job queue. Revisit if a large
collection makes the request time out; nothing here blocks adding an async path later.
Full-collection `.colpkg` export isn't scoped yet — not required by any Phase 1 step, open if
demand shows up.

---

## Media — `media.go` (Phase 1, step 8 — blob store itself is [#60](https://github.com/Jolls/enshu/issues/60))

| Method | Path | Permission | Purpose |
|---|---|---|---|
| GET | `/media/{sha256}` | `can_view` (of a deck referencing it) | Serve a blob from the content-addressed filesystem store; long-lived cache headers, since the address is the hash |

---

## Explicitly not routed

Mirrors architecture.md §11 "Explicitly not doing":

- No sync-protocol endpoints (invariant §2.9).
- No filtered/custom-study deck routes.
- No plugin/add-on routes.
- No public-deck-directory / browse-other-users'-decks route — reachability is `deck_access`
  only, no exceptions (CLAUDE.md §9).
- No ops routes (health check, metrics) in this table — add when deploy tooling is scaffolded,
  not before.

---

## Open questions

Collected from above, so they're visible in one place:

1. **Note-type read access under sharing** (Notes/note-types section) — a user with only
   `can_view`/`can_study` on a deck has no read path to a note type they don't own that backs
   one of its notes.
2. **Card preview route** — not included. Add only if template authoring needs a live preview
   before a note exists to generate one from.

**Explicitly not routed:** no account-deletion route. User deletion is blocked at the FK level
by design ([#51](https://github.com/Jolls/enshu/issues/51)) — a user's decks, notes, note types,
access grants, scheduling state, and review history all restrict. Account closure needs an
ownership-transfer decision for shared decks and a written `review_log` decision first.
