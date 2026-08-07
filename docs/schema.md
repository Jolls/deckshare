# Data model

Extracted from [CLAUDE.md](../CLAUDE.md) §5. Read this before any schema change, new table,
or query that crosses the content/per-user-state boundary.

Drizzle schema in `src/lib/server/db/schema.ts` is the source of truth; the SQL here is the
intended shape and the reasoning behind it, not the literal file. When they diverge, fix
whichever one is wrong — but they should not diverge silently, so update this file in the
same PR as the migration.

**The invariant this whole file exists to protect:** scheduling state never lives on the
`cards` row. It lives in `user_card_state`, primary-keyed `(user_id, card_id)`. Every
multiuser feature is a consequence of that. See CLAUDE.md §2.

---

## Identifiers

**UUIDv7 for everything, generated client-side where possible.** Not `serial`, not Anki's
epoch-millis ints. Rationale:

- The client generates `review_log` rows *before* they reach the server. A client-generated
  id makes retry idempotent (`ON CONFLICT (id) DO NOTHING`) with no dedup logic.
- Time-ordered, so it indexes like a sequence rather than random UUIDv4.
- No enumeration of other users' resources from an integer id.

Anki's original numeric ids are preserved as `anki_id` columns on imported rows — needed for
round-trip export fidelity, never used as a key.

---

## Content (shared, one copy per deck)

```sql
note_types  id, owner_id, name, css, is_cloze, sort_field_idx, anki_id
fields      id, note_type_id, ordinal, name, font, size, is_rtl, sticky
templates   id, note_type_id, ordinal, name, qfmt, afmt, browser_qfmt, browser_afmt

notes       id, guid, note_type_id, deck_id, fields jsonb, tags text[],
            checksum bigint, created_at, modified_at, anki_id
            -- UNIQUE (deck_id, guid)   <- makes re-import idempotent
            -- fields is an ordered array, indexed by fields.ordinal

cards       id, note_id, template_id, ordinal, deck_id, anki_id
            -- content addressing ONLY. No due, no ivl, no factor, no state.
            -- UNIQUE (note_id, ordinal)

decks       id, owner_id, name, description, visibility, forked_from_deck_id,
            preset jsonb, created_at, modified_at
            -- visibility: 'private' | 'public'
deck_access deck_id, user_id, role, created_at   -- role: 'owner' | 'editor' | 'viewer'
            -- PRIMARY KEY (deck_id, user_id)
```

`notes.fields` as `jsonb` (ordered array of strings) rather than a `note_fields` table:
fields are always read and written as a unit with the note, never queried individually, and
the row count would otherwise be 5–10× the note count.

**Deferred:** full-text search over notes. The intended shape is a generated `tsvector`
column over the concatenated fields, but nothing queries it yet and it is not implemented.
Add it with the search feature, not before.

`notes.guid` is Anki's stable per-note identifier and the idempotency key for import.
It must be present on every note from day one — retrofitting it duplicates every early
user's decks on their next import.

---

## Per-user state

```sql
user_card_state  user_id, card_id,
                 due timestamptz, stability float8, difficulty float8,
                 state smallint,           -- ts-fsrs State: 0 new,1 learning,2 review,3 relearning
                 reps int, lapses int, elapsed_days int, scheduled_days int,
                 learning_steps smallint,
                 last_review timestamptz, suspended bool, buried_until date,
                 flag smallint
                 -- PRIMARY KEY (user_id, card_id)
                 -- INDEX (user_id, due) WHERE NOT suspended   <- the queue query

review_log       id uuid,                  -- client-generated UUIDv7
                 user_id, card_id, rating smallint,
                 reviewed_at timestamptz, duration_ms int,
                 state_before smallint, learning_steps_before smallint,
                 stability_before float8, difficulty_before float8,   -- NULL for imported history
                 elapsed_days_before int, scheduled_days_after int,
                 fsrs_version smallint,     -- NULL for imported history
                 review_kind smallint
                 -- INDEX (user_id, reviewed_at)
                 -- append-only; the optimiser's training set
                 -- FKs are ON DELETE RESTRICT: nothing may cascade training data away

user_fsrs_params id, user_id, deck_id NULL,   -- deck_id NULL = the user's global default
                 fsrs_version smallint, params jsonb,
                 desired_retention float8, optimised_at, review_count_at_fit
                 -- UNIQUE (user_id, deck_id) with NULL treated as a value
                 -- surrogate `id` PK; the pair above is the real key

users            id, email, display_name, timezone, day_start_hour smallint DEFAULT 4,
                 created_at
                 -- UNIQUE on lower(email): one account per address, any casing
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

**`stability` and `difficulty` are `double precision`, not `real`.** `ts-fsrs` rounds them to
8 decimal places and clamps stability to 36500; `real` holds ~7 significant digits, so a
value round-tripped through it would not byte-match what the replay path computes in memory.
That is precisely the silent client/server interval disagreement CLAUDE.md §3 warns about.

`user_card_state.learning_steps` mirrors `ts-fsrs`'s `Card.learning_steps`. FSRS-6 short-term
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
media_blobs  sha256 pk, size_bytes, mime, created_at    -- bytes on disk/S3 at sha256 path
media_refs   deck_id, filename, sha256                  -- PRIMARY KEY (deck_id, filename)
```

Anki references media by filename inside note fields (`<img src="x.jpg">`), so the
`(deck_id, filename)` mapping is what rendering resolves against. Two decks shipping an
identical image store one blob.

Storage backend for self-hosters (filesystem vs S3-compatible) is undecided; content
addressing makes it swappable, so it can wait.

---

## The day boundary

Anki's "due today" is not midnight UTC — it is a per-user rollover hour (default 04:00
local) so late-night study counts as the previous day. `users.timezone` +
`users.day_start_hour` drive it. Compute the queue window in the query, not in the client,
so a user crossing a timezone doesn't see a phantom empty queue.

`src/lib/server/db/day-boundary.ts` builds the two SQL expressions every queue query needs:
`studyDayStart()` and `studyDayEnd()`. A card counts as due when `user_card_state.due <
studyDayEnd(...)`. The arithmetic runs on the local wall clock so a DST transition makes the
study day 23 or 25 hours long rather than silently shifting the rollover.

---

## Access control at the query layer

Every query touching a deck takes a `user_id` and joins `deck_access`. Route guards are not
sufficient — a shared deck means "readable by some users" is the normal case, not the
exception.

No cross-user reads without a `deck_access` row. The only exception is
`decks.visibility = 'public'`, and that grants read of *content*, never of another user's
`user_card_state`.

---

## Migrations

Generated by `drizzle-kit`, committed, and **immutable once merged**. Fix forward with a new
migration; never edit an applied one.

Checklist for a new table:

1. Add it to `src/lib/server/db/schema.ts`.
2. Generate the migration; commit the generated SQL unedited.
3. Verify it applies cleanly to a fresh database.
4. Update this file's table listing and, if it holds per-user state, confirm the
   `(user_id, …)` key and index shape.
