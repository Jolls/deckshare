# Data model

Extracted from [architecture.md](architecture.md) §5. Read this before any schema change, new table,
or query that crosses the content/per-user-state boundary.

The committed migration SQL (`migrations/`, architecture.md §4) is the source of truth; the
SQL here is the intended shape and the reasoning behind it, not the literal file. When they
diverge, fix whichever one is wrong — but they should not diverge silently, so update this
file in the same PR as the migration.

**The invariant this whole file exists to protect:** scheduling state never lives on the
`cards` row. It lives in `user_card_state`, primary-keyed `(user_id, card_id)`. Every
multiuser feature is a consequence of that. See CLAUDE.md §2.

---

## Identifiers

**UUIDv7 for everything, generated client-side where possible.** Not `serial`, not Anki's
epoch-millis ints. Rationale:

- The client generates the *id* of a `review_log` row before it reaches the server, which
  makes retry idempotent (`ON CONFLICT (id) DO NOTHING`) with no dedup logic. Only the id: the
  row's contents are derived server-side (CLAUDE.md §2.7), so an id is the one part of a
  review a client is trusted with — and it is trusted with it precisely because a forged or
  repeated one can only cause the server to discard a duplicate.
- Time-ordered, so it indexes like a sequence rather than random UUIDv4.
- No enumeration of other users' resources from an integer id.

Anki's original numeric ids are preserved as `anki_id` columns on imported rows — needed for
round-trip export fidelity, never used as a key.

**Re-import dedups on name, not on `anki_id`.** Anki ids are per-collection: deck id 1 is
`Default` in every collection that has ever existed, and note-type ids are creation
timestamps unique only within one profile. `UNIQUE (owner_id, anki_id)` would therefore
assert "one user can only ever own one deck with Anki id 1", and the second import would
silently upsert into the first import's deck — merging two unrelated collections
unrecoverably. It would also merge two genuinely different note types, which renders every
field into the wrong slot (`notes.fields` is positional, indexed by `fields.ordinal`).

So the keys are:

| Table | Key | Why |
|---|---|---|
| `decks` | `UNIQUE (owner_id, name)` | Names are unique by full path within a collection, so re-importing an updated export still matches. A cross-collection collision becomes "import into the deck of that name" — what Anki does and what a user expects. |
| `note_types` | `UNIQUE (owner_id, name)` | Matches Anki's own reconcile rule. |
| `notes` | `UNIQUE (owner_id, guid)` | `guid` is globally unique in Anki, so this doesn't need deck dedup to land first. |
| `review_log` | `UNIQUE (user_id, card_id, anki_id)` | The one `anki_id` worth keying on: `revlog.id` is epoch-millis and identifies the row within its collection. `user_id` leads because cards are shared content while reviews are per-user training data (§2.5) — one user's imported history must never block another's. |

`notes.owner_id` is denormalised from `decks.owner_id` because a unique index can't span a
join. It must not drift: moving a note between decks sets it to the new deck's owner. A no-op
in Phase 1, load-bearing once decks are shared. Nothing enforces the equality at the database
level today — it holds by convention in the query layer, which is weaker than it should be for
an import key.

`anki_id` is nullable everywhere and NULLs stay distinct, so rows authored here rather than
imported collide on nothing — including every row the live grading path (`internal/review/`,
architecture.md §4) writes, which never sets one.

Because `(owner_id, name)` is a real constraint, a user reusing a deck or note-type name is
an ordinary client error: the query layer (`internal/db/`, architecture.md §4) raises a
duplicate-name error and handlers answer 409, never a 500.

---

## Content (shared, one copy per deck)

```sql
note_types  id, owner_id, name, css, is_cloze, sort_field_idx, anki_id
            -- UNIQUE (owner_id, name)   <- re-import reuses the owner's note type
fields      id, note_type_id, ordinal, name, font, size, is_rtl, sticky
templates   id, note_type_id, ordinal, name, qfmt, afmt, browser_qfmt, browser_afmt

notes       id, guid, owner_id, note_type_id, deck_id, fields jsonb, tags text[],
            checksum bigint, created_at, modified_at, anki_id
            -- UNIQUE (owner_id, guid)   <- makes re-import idempotent
            -- INDEX (deck_id)           <- deck-scoped queries + the deck-delete RI check
            -- owner_id is denormalised from decks.owner_id; a unique index can't span a join
            -- fields is an ordered array, indexed by fields.ordinal

cards       id, note_id, template_id, ordinal, deck_id, anki_id
            -- content addressing ONLY. No due, no ivl, no factor, no state.
            -- UNIQUE (note_id, ordinal)

decks       id, owner_id, name, description,
            preset jsonb, created_at, modified_at, anki_id
            -- UNIQUE (owner_id, name)   <- re-import reuses the owner's deck of that name
deck_access deck_id, user_id, created_at,
            can_view bool, can_study bool, can_edit_content bool,
            can_edit_settings bool, can_manage_access bool, can_delete bool
            -- PRIMARY KEY (deck_id, user_id)
            -- the ONLY thing that makes a deck reachable by a second user
            -- six independent per-(user, deck) permissions, not a role enum -- see below
```

`notes.fields` as `jsonb` (ordered array of strings) rather than a `note_fields` table:
fields are always read and written as a unit with the note, never queried individually, and
the row count would otherwise be 5–10× the note count.

**Editing a note's fields must not regenerate its cards by dropping and recreating them, once
`cards` rows can carry scheduling state or review history.** A naive edit handler — delete every
`cards` row for the note, reinsert from the new field values — is the obvious first
implementation and it is a trap: as soon as a card can have a `user_card_state` row or a
`review_log` row, that delete either hits the `ON DELETE RESTRICT` on `review_log.card_id` and
the edit fails outright, or (for a card with scheduling state but no reviews yet) cascade-deletes
its `user_card_state` silently, discarding a live card's progress as a side effect of an
unrelated field edit. This is the CLAUDE.md §15 "silently corrupts `user_card_state`" bucket —
it earns `sev: critical` the day it's reachable. It's only harmless before the reviewer and
`.apkg` import exist to populate either table, which is exactly why it's easy to ship first and
forget: nothing fails in Phase 1's early CRUD-only state. The fix belongs to whichever change
first makes cards stateful (the reviewer, §11 step 7, or import, step 8): diff the old and new
cloze ordinals (or template set, for non-cloze note types) and only add/remove the cards that
actually changed, leaving every untouched card's row — and its scheduling state — alone.

**Deferred:** full-text search over notes. The intended shape is a generated `tsvector`
column over the concatenated fields, but nothing queries it yet and it is not implemented.
Add it with the search feature, not before.

`notes.guid` is Anki's stable per-note identifier and, paired with `owner_id`, the
idempotency key for import. It must be present on every note from day one — retrofitting it
duplicates every early user's decks on their next import. One guid is one note per owner, not
per deck: re-importing the same collection into a second deck finds the existing note rather
than forking its identity.

`deck_access` grants six independent permissions per `(user_id, deck_id)` — a row can hold any
combination, there is no role enum and no implied hierarchy:

| Flag | Grants |
|---|---|
| `can_view` | See the deck, its notes/cards, and rendered content |
| `can_study` | Fetch review batches, grade cards (writes only the caller's own `user_card_state`/`review_log`), set a personal per-deck FSRS retention override |
| `can_edit_content` | Create/edit/delete notes and their generated cards, move notes between decks, import `.apkg` into the deck |
| `can_edit_settings` | Edit deck metadata (name, description, preset), export the deck |
| `can_manage_access` | Grant/revoke/change other users' `deck_access` rows |
| `can_delete` | Delete the deck |

`can_view` is a practical prerequisite for the other five to mean anything, but that's an
application-level convention (a grant form defaults it on alongside any other flag) — nothing
at the database level enforces the nesting, by design. The full per-route mapping lives in
[routes.md](routes.md).

A deck's creator gets all six flags on creation. A personal, single-user deck is just the
trivial case of this — one user, fully permissioned — not a separate code path.

**Open guard, not yet enforced:** nothing currently blocks removing the last
`can_manage_access` (or `can_delete`) holder from a deck, which would strand it with no one
able to manage access or delete it. Needs a check before Phase 2's access-management routes
ship.

---

## Per-user state

```sql
user_card_state  user_id, card_id,
                 due timestamptz, stability float8, difficulty float8,
                 state smallint,           -- FSRS State: 0 new,1 learning,2 review,3 relearning
                 reps int, lapses int, elapsed_days int, scheduled_days int,
                 learning_steps smallint,
                 last_review timestamptz, suspended bool, buried_until date,
                 flag smallint
                 -- PRIMARY KEY (user_id, card_id)
                 -- INDEX (user_id, due) WHERE NOT suspended   <- the queue query

review_log       id uuid,                  -- client-generated UUIDv7; every OTHER column is
                                           -- computed server-side (CLAUDE.md §2.7)
                 user_id, card_id, rating smallint,
                 reviewed_at timestamptz, duration_ms int,
                 state_before smallint, learning_steps_before smallint,
                 stability_before float8, difficulty_before float8,   -- NULL for imported history
                 elapsed_days_before int, scheduled_days_after int,
                 fsrs_version smallint,     -- NULL for imported history
                 review_kind smallint,
                 anki_id bigint             -- revlog.id; NULL for rows our reviewer produced
                 -- INDEX (user_id, reviewed_at)
                 -- UNIQUE (user_id, card_id, anki_id)   <- re-import dedup
                 -- append-only; the optimiser's training set
                 -- FKs are ON DELETE RESTRICT: nothing may cascade training data away

user_fsrs_params id, user_id, deck_id NULL,   -- deck_id NULL = the user's global default
                 fsrs_version smallint, params jsonb,
                 desired_retention float8, optimised_at, review_count_at_fit
                 -- UNIQUE (user_id, deck_id) with NULL treated as a value
                 -- surrogate `id` PK; the pair above is the real key

users            id, email, password_hash, display_name, timezone,
                 day_start_hour smallint DEFAULT 4, created_at
                 -- UNIQUE on lower(email): one account per address, any casing
                 -- password_hash is argon2id (@node-rs/argon2), never a weaker algorithm

sessions         id text pk,             -- SHA-256 hex of the session token; the raw token
                                          -- lives only in the cookie, never in the database
                 user_id, expires_at, created_at
                 -- INDEX (user_id)      -- to invalidate all of a user's sessions
```

**CHECK constraints.** A handful of columns produce wrong schedules rather than errors when
given a bad value, so the database rejects them: `users.day_start_hour` 0–23 (it is fed
straight into `make_interval`), `review_log.rating` 1–4, `review_log.review_kind` 0–4,
`review_log.state_before` and `user_card_state.state` 0–3, and
`user_fsrs_params.desired_retention` strictly between 0 and 1.

**Indexes backing `ON DELETE RESTRICT`.** A restricting foreign key makes Postgres run
`SELECT 1 FROM child WHERE fk = $1` on every parent delete, so the referencing column needs
to lead an index or that becomes a sequential scan. `review_log` carries
`(card_id, user_id, reviewed_at)` for this — `card_id` leads for the RI check, and the
trailing columns also serve the per-card replay path. `cards.template_id` and
`notes.note_type_id` are indexed for the same reason.

**`stability` and `difficulty` are `double precision`, not `real`.** FSRS rounds them to
8 decimal places and clamps stability to 36500; `real` holds ~7 significant digits, so a
value round-tripped through it would not byte-match what the batch-preview and grade-time
`Repeat()` calls compute in memory (architecture.md §6, CLAUDE.md §10.2) — the consistency
check between them needs exact values to compare, not ones already degraded by storage.

`user_card_state.learning_steps` mirrors `go-fsrs`'s `Card.LearningSteps`. FSRS-6 short-term
scheduling reads it, so it has to survive a reload or a `review_log` replay.

`review_log` rows carry `*_before` values and `fsrs_version` so the log stays interpretable
after a parameter refit or an algorithm upgrade — without them, old rows can't be replayed.

`review_log` is **append-only**. It is the optimiser's training data, not bookkeeping. No
`DELETE` path without a written decision.

`user_fsrs_params.params` is a JSON array plus an explicit `fsrs_version` integer — never
fixed-width columns. Parameter count changes upstream: 17 in FSRS-4.5, 19 in FSRS-5, 21 in
FSRS-6. The version column also keeps old fitted parameters readable after an upgrade.

Parameters are per-user, optionally per `(user, deck)`. **Never fit one parameter set across
a cohort** — memory behaviour is individual, and a class-wide fit is wrong for every member
of it.

---

## Media

Content-addressed and deduplicated across all users:

```sql
media_blobs  sha256 pk, size_bytes, mime, created_at    -- metadata only; bytes live on disk
media_refs   deck_id, filename, sha256                  -- PRIMARY KEY (deck_id, filename)
```

Anki references media by filename inside note fields (`<img src="x.jpg">`), so the
`(deck_id, filename)` mapping is what rendering resolves against. Two decks shipping an
identical image store one blob.

**A filename collision within one import — one name, two different contents — is survivable, not
fatal.** It happens honestly (two source decks that both happen to name a file `image1.jpg`) and
it also happens as a side effect of NFC-normalising filenames on read (§7): two names distinct
only in Unicode normalisation form collapse onto one. Aborting an import of several hundred files
over one colliding name is the wrong trade against the size of the problem, so the policy is
first-seen wins — the first file under that name (in the package's own media-index order, so it's
deterministic across re-imports of the same package) is kept, the rest are dropped, and the
import reports a warning naming the dropped files. Same bytes twice needs no warning at all; it's
only a genuine content disagreement that does.

**Storage backend is settled: the filesystem**, content-addressed, at
`${MEDIA_ROOT}/<sha[0:2]>/<sha>`. The database holds metadata rows only and never a `bytea`
column. S3-compatible stays a drop-in later — the metadata-row-plus-external-bytes shape is
identical, only "external" changes. See architecture.md §12; the store itself is not built yet
([#60](https://github.com/Jolls/enshu/issues/60)).

---

## The day boundary

Anki's "due today" is not midnight UTC — it is a per-user rollover hour (default 04:00
local) so late-night study counts as the previous day. `users.timezone` +
`users.day_start_hour` drive it. Compute the queue window in the query, not in the client,
so a user crossing a timezone doesn't see a phantom empty queue.

The query layer (`internal/db/`, architecture.md §4) builds the two SQL expressions every
queue query needs: `StudyDayStart()` and `StudyDayEnd()`. A card counts as due when
`user_card_state.due < StudyDayEnd(...)`. The arithmetic runs on the local wall clock so a DST
transition makes the study day 23 or 25 hours long rather than silently shifting the rollover.

---

## Access control at the query layer

Every query touching a deck takes a `user_id` and joins `deck_access`. Handler-level guards are
not sufficient — a shared deck means "readable by some users" is the normal case, not the
exception.

**No cross-user reads without a `deck_access` row, and there are no exceptions.** There is no
visibility flag and no public-deck carve-out: a deck is reachable by exactly the users holding
a row, and no combination of that row's permission flags ever grants read of another user's
`user_card_state`. One authorisation path means one thing to get right and one thing to test.

**A deck that exists but the caller can't see should respond identically to a deck that doesn't
exist — 404, not 403.** Collapsing "not found" and "found but forbidden" into one outcome at the
query layer (rather than distinguishing them and letting a handler turn the distinction into two
different status codes) is what stops a 403 from becoming an existence oracle: a user who lacks
`can_view` on someone else's deck must not be able to learn the deck *exists* by getting a
different answer than they'd get for a deck id that was never valid at all.

`user_card_state` and `review_log` are per-user throughout. A deck's content is shared; nobody's
progress on it ever is.

---

## Migrations

Committed SQL, **immutable once merged**. Fix forward with a new migration; never edit an
applied one. Migration tool is not yet chosen — see architecture.md §12 — but the checklist
below holds regardless of which one lands, since `sqlc` reads the committed SQL directly
rather than generating it from a Go-side schema.

Checklist for a new table:

1. Write the migration; commit the SQL unedited. **One exception, before merge only:** a
   `NOT NULL` column added to an existing table. A bare `ADD COLUMN ... NOT NULL` applies fine
   to an empty database and fails on every database that has rows. Split it into
   add-nullable → backfill → `SET NOT NULL` by hand and cover it with a test that migrates a
   *populated* database, since the fresh-database test cannot see the failure.
2. Verify it applies cleanly to a fresh database.
3. Update this file's table listing and, if it holds per-user state, confirm the
   `(user_id, …)` key and index shape.
