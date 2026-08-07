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
deck_access deck_id, user_id, role      -- 'owner' | 'editor' | 'viewer'
            -- PRIMARY KEY (deck_id, user_id)
```

`notes.fields` as `jsonb` (ordered array of strings) rather than a `note_fields` table:
fields are always read and written as a unit with the note, never queried individually, and
the row count would otherwise be 5–10× the note count. Search uses a generated `tsvector`
column over the concatenation.

`notes.guid` is Anki's stable per-note identifier and the idempotency key for import.
It must be present on every note from day one — retrofitting it duplicates every early
user's decks on their next import.

---

## Per-user state

```sql
user_card_state  user_id, card_id,
                 due timestamptz, stability real, difficulty real,
                 state smallint,           -- ts-fsrs State: 0 new,1 learning,2 review,3 relearning
                 reps int, lapses int, elapsed_days int, scheduled_days int,
                 last_review timestamptz, suspended bool, buried_until date,
                 flag smallint
                 -- PRIMARY KEY (user_id, card_id)
                 -- INDEX (user_id, due) WHERE NOT suspended   <- the queue query

review_log       id uuid,                  -- client-generated UUIDv7
                 user_id, card_id, rating smallint,
                 reviewed_at timestamptz, duration_ms int,
                 state_before smallint, stability_before real, difficulty_before real,
                 elapsed_days_before int, scheduled_days_after int,
                 fsrs_version smallint, review_kind smallint
                 -- INDEX (user_id, reviewed_at)
                 -- append-only; the optimiser's training set

user_fsrs_params user_id, deck_id NULL,    -- NULL = the user's global default
                 fsrs_version smallint, params jsonb,
                 desired_retention real, optimised_at, review_count_at_fit
                 -- UNIQUE (user_id, deck_id) with NULL treated as a value

users            id, email, display_name, timezone, day_start_hour smallint DEFAULT 4,
                 created_at
```

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
