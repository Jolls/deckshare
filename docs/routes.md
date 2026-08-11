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
- **Role** is the minimum `deck_access.role` the query layer must enforce (CLAUDE.md §9) —
  never a handler-level guard alone. Blank means not deck-scoped: session-only, or gated by
  row ownership instead (e.g. a note type's `owner_id`).
- **Phase / step** cites architecture.md §11's build order, so route work can be sequenced
  against it.

**Assumption, not settled elsewhere:** `owner > editor > viewer`, and specifically — *viewer*
can study (write their own `user_card_state`/`review_log`) and read content; *editor* can
additionally write notes/cards; *owner* can additionally write deck settings, `deck_access`,
and delete the deck. Nothing in architecture.md or schema.md pins this down yet; it's inferred
from the role names on `deck_access`. Flag if that's wrong before Phase 2 gates on it.

---

## Auth — `auth.go` (Phase 1, step 3)

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/signup` | public | Signup form |
| POST | `/signup` | public | Create account, `argon2id`-hash password, start session |
| GET | `/login` | public | Login form |
| POST | `/login` | public | Verify credentials, create session (cookie: hashed token, §12) |
| POST | `/logout` | session | Destroy session |
| GET | `/` | public | Redirect to `/decks` if authed, else `/login` |

---

## Decks — `decks.go` (Phase 1, step 5)

| Method | Path | Role | Purpose |
|---|---|---|---|
| GET | `/decks` | — | List decks reachable via `deck_access` |
| GET | `/decks/new` | — | New-deck form |
| POST | `/decks` | — | Create deck; creator gets a `deck_access` row with role `owner` |
| GET | `/decks/{id}` | viewer | Detail: notes list, card/due counts |
| GET | `/decks/{id}/edit` | editor | Edit form (name, description, preset) |
| POST | `/decks/{id}/edit` | editor | Update |
| POST | `/decks/{id}/delete` | owner | Delete — currently unreachable behind FK restricts, see [#15](https://github.com/Jolls/enshu/issues/15) |

---

## Note types — `notetypes.go` (Phase 1, step 5)

Owner-scoped (`note_types.owner_id`), not deck-scoped — a note type is reusable across all of
its owner's decks.

| Method | Path | Role | Purpose |
|---|---|---|---|
| GET | `/note-types` | owns row | List the caller's own note types |
| GET | `/note-types/new` | — | New note-type form (fields + templates builder) |
| POST | `/note-types` | — | Create |
| GET | `/note-types/{id}/edit` | owns row | Edit form |
| POST | `/note-types/{id}/edit` | owns row | Update fields/templates/CSS |
| POST | `/note-types/{id}/delete` | owns row | Delete — blocked while any note references it |

**Open question — not a route table decision, needs a call before Phase 2:** rendering a note
in a shared deck requires reading its note type's fields/templates, but `note_types` has no
`deck_access`-style row — only `owner_id`. A viewer with `deck_access` on a deck whose notes use
someone else's note type currently has no path to read it. Either note-type read needs a second
authorization path ("owns it, OR it backs a note in a deck I have access to"), or note types
need to be copied/forked into the sharing deck's own scope on first share. Neither is decided.

---

## Notes — `notes.go` (Phase 1, step 5)

Cards have **no routes of their own** — they're generated from a note × its note type's
templates (one per template, N per cloze ordinal — architecture.md §8) and destroyed/regenerated
as a side effect of note writes.

| Method | Path | Role | Purpose |
|---|---|---|---|
| GET | `/decks/{deckId}/notes/new` | editor | New-note form (choose note type, fill fields) |
| POST | `/decks/{deckId}/notes` | editor | Create note; generates its cards |
| GET | `/notes/{id}/edit` | editor | Edit form |
| POST | `/notes/{id}/edit` | editor | Update fields/tags; regenerates cards if cloze ordinals changed |
| POST | `/notes/{id}/delete` | editor | Delete note and its cards |
| POST | `/notes/{id}/move` | editor | Change `deck_id`; must also update denormalised `owner_id` (schema.md, "must not drift") |

---

## Review — `review.go` (Phase 1, step 7)

The core loop — full contract in architecture.md §6. Table here is deliberately thin; don't
duplicate the pseudocode.

| Method | Path | Role | Purpose |
|---|---|---|---|
| GET | `/decks/{id}/review` | viewer | Reviewer page. First batch (20 cards), the precomputed 4-rating outcome per card, the user's FSRS params, and the study-day end are rendered inline in the response — no separate request for card 1 (§6) |
| GET | `/api/reviews/next` | viewer | Refill batch, JSON. Keyset cursor `(due, cardId)` + deck id; same per-card payload shape as the inline batch; server excludes cards already reviewed this study day (§6) |
| POST | `/api/reviews/batch` | viewer | Grade. `{events:[{id,cardId,rating,reviewedAt,durationMs}]}` — exactly these fields, idempotent, returns `<after>` per event. See §6 for the full authorise/recompute/store sequence and the concurrency mechanisms |

---

## Access — `access.go` (Phase 2)

Schema (`deck_access`) exists from Phase 1 step 2 even though nothing grants a second user
access until Phase 2 (architecture.md §11).

| Method | Path | Role | Purpose |
|---|---|---|---|
| GET | `/decks/{id}/access` | owner | List collaborators and roles |
| POST | `/decks/{id}/access` | owner | Grant access (email + role) |
| POST | `/decks/{id}/access/{userId}/edit` | owner | Change a collaborator's role |
| POST | `/decks/{id}/access/{userId}/delete` | owner | Revoke access |

**Open question:** no guard yet against removing the last `owner` row on a deck, which would
strand it with no one able to manage access or delete it.

---

## Settings — `settings.go` (Phase 1: account settings step 3, FSRS default step 9)

| Method | Path | Role | Purpose |
|---|---|---|---|
| GET | `/settings` | session | Profile (display name, timezone, `day_start_hour`), password change, global FSRS default (`desired_retention` where `deck_id IS NULL`) |
| POST | `/settings` | session | Update profile |
| POST | `/settings/password` | session | Change password |
| POST | `/settings/fsrs` | session | Update the global `desired_retention` default |
| POST | `/decks/{id}/settings/fsrs` | viewer | Per-deck override. Scoped to the caller, not the deck — `user_fsrs_params` keys on `(user_id, deck_id)`, so this is "my retention target for this deck," not a deck-wide setting an owner sets for everyone |

---

## Import / export — `apkg.go` (Phase 1, step 8)

| Method | Path | Role | Purpose |
|---|---|---|---|
| GET | `/import` | session | Upload form |
| POST | `/import` | session | Upload `.apkg`; synchronous `read → IR → db`, redirects to the resulting deck. Idempotent on `(owner_id, guid)` (§7, invariant §2.2) |
| GET | `/decks/{id}/export` | editor | Stream a `.apkg` (`db → IR → write`, `Content-Disposition: attachment`) |

Synchronous upload is a Simplicity First choice for MVP — no job queue. Revisit if a large
collection makes the request time out; nothing here blocks adding an async path later.
Full-collection `.colpkg` export isn't scoped yet — not required by any Phase 1 step, open if
demand shows up.

---

## Media — `media.go` (Phase 1, step 8 — blob store itself is [#34](https://github.com/Jolls/enshu/issues/34))

| Method | Path | Role | Purpose |
|---|---|---|---|
| GET | `/media/{sha256}` | viewer (of a deck referencing it) | Serve a blob from the content-addressed filesystem store; long-lived cache headers, since the address is the hash |

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

1. **Note-type read access under sharing** (Notes/note-types section) — a viewer with
   `deck_access` has no read path to a note type they don't own.
2. **Last-owner-removal guard** on `deck_access` (Access section) — unguarded today.
3. **Card preview route** — not included. Add only if template authoring needs a live preview
   before a note exists to generate one from.
