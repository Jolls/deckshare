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

Every `uuid` PK also carries `DEFAULT uuidv7()` (Postgres 18) as a safety net: the application
still generates ids in Go (`uuid.NewV7()`) at insert time and supplies them explicitly wherever
the id needs to be known before the row exists — the DB default only fires on an insert that
forgot to supply one, and produces a valid, time-ordered id when it does. It is not a change to
"generated client-side where possible," just a belt-and-braces fallback.

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
in Phase 1, load-bearing once decks are shared. As of migration `00015` (#54), the equality is
enforced at the database level: `decks` carries `UNIQUE (id, owner_id)` and `notes` a composite
`FOREIGN KEY (deck_id, owner_id) REFERENCES decks (id, owner_id)`. A `CHECK` can't subquery
another table and a generated column can't reference another row, so a composite FK is the only
non-procedural mechanism Postgres offers for this shape — declarative, and referential-integrity
row locking makes it race-free against a concurrent deck move, which a trigger would need to
reimplement by hand.

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

`decks.preset` holds Anki's `dconf` shape, extended with fields of our own:
`{"new": {"perDay": 20}, "rev": {"perDay": 200, "order": "due"}, "priority": "due",
"due": {"lookAheadMinutes": 0}}` — the per-deck daily new-card ceiling (#101), the deck's combined
new+due daily total (`rev.perDay`, originally an independent due-only cap under #115, redefined
by [#118](https://github.com/Jolls/enshu/issues/118) as the shared total), review order (#116),
review prioritization (`priority`, #118 — which side of the new/due split fills the shared total
first, the other side backfilling the remainder; top-level rather than nested under `new` since it
governs the whole day's split rather than describing new-card mixing the way its predecessor,
`new.mix`, did), and the due-date look-ahead window (#154, not an Anki `dconf` field). Each field
is independently optional; absent, malformed, or out-of-range reads as its default
(`new.perDay`/`rev.perDay`: Anki's own 0..9999 range, default 20/200; `rev.order`: `due`;
`priority`: `due`; `due.lookAheadMinutes`: 0..1440, default 0). Parsing happens in Go
(`internal/review.NewPerDay`/`RevPerDay`/`ParseRevOrder`/`ParsePriority`/`DueLookAheadMinutes`),
never in SQL, so a malformed value degrades to the default instead of failing a study fetch. The
new/due split itself (`PriorityAllocate`) is also Go-side, used by the "left to study" display;
`ListDueCardsForStudy`/`ListReviewCardsForStudy`/`ListNewCardsForStudy` achieve the same split at
fetch time via ordering + per-side cutoffs + a `LIMIT` clamped to the remaining total, not by
calling `PriorityAllocate` directly (docs/architecture.md §6/§20 has the reasoning).
`CountQueueForDeck`/`CountQueueForUser` still report the deck's raw unseen-card count, uncapped
by `new.perDay`/`rev.perDay` ([#106](https://github.com/Jolls/enshu/issues/106)), but do apply
`due.lookAheadMinutes` to their due-card filter same as the study queries. `CountQueueForUser`
groups counts across every deck a user can view in one query, so it can't bind
`look_ahead_minutes` as a single scalar the way the other three do — the caller passes parallel
`deck_ids`/`look_ahead_minutes` arrays (each value parsed in Go same as everywhere else) and the
query unnests them into a per-deck join.

`notes.fields` as `jsonb` (ordered array of strings) rather than a `note_fields` table:
fields are always read and written as a unit with the note, never queried individually, and
the row count would otherwise be 5–10× the note count.

**Editing a note's fields must not regenerate its cards by dropping and recreating them.** A
naive edit handler — delete every `cards` row for the note, reinsert from the new field
values — is the obvious first implementation and it is a trap: `user_card_state.card_id`
cascades on card delete (#51), so that delete silently discards a live card's progress, and a
re-created card gets a new id, stranding its `review_log` history from it. This is the
CLAUDE.md §15 "silently corrupts `user_card_state`" bucket — it earns `sev: critical` the day
it's reachable. Implemented in `internal/db/cards.go`'s `SyncNoteCards` (#54): it diffs the old
and new cloze ordinals (or template set, for non-cloze note types) and only adds/removes the
cards that actually changed, leaving every untouched card's row — and its scheduling state —
alone.

Changing a note's `note_type_id` (#138) reuses this same diff rather than a bespoke path: the
desired card set is computed from the *target* note type's templates, and any surviving ordinal
whose template differs from what its card currently points at has `cards.template_id` repointed
in place — needed because the study batch query reads `qfmt`/`afmt` from `cards.template_id`, not
from `notes.note_type_id`, so a stale pointer would render the old note type's template. An
ordinal that doesn't survive the change (e.g. dropping "Basic (and reversed card)" down to
"Basic") is hard-deleted the same way a shrunk cloze note's card already is today: `user_card_state`
cascades away with it, and any `review_log` rows are left in place, orphaned but intact, per the
no-FK decision below (Deletion policy).

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

**Last-holder guard — enforced in the query layer.** A deck must always retain at least one
`can_manage_access` holder and one `can_delete` holder, or it is stranded with nobody able to
manage access or delete it. `internal/db/deletion.go`'s `RevokeDeckAccess` and `SetDeckAccess`
apply the mutation and then re-count holders inside the same transaction, under a row lock on
the deck, and return `ErrLastAccessHolder` for the caller to roll back on. It is an
authorisation rule, so it lives where the others live (CLAUDE.md §9) rather than in a trigger
— and a trigger would need the same lock to be race-free anyway. Consequence: a deck's sole
member cannot revoke their own access; they delete the deck. `decks.owner_id` is not a
permission source and exempts nobody.

---

## Deletion policy

Settled in [#51](https://github.com/Jolls/enshu/issues/51); full reasoning:
`docs/plans/51-deletion-policy.md`. Two organising principles decide every foreign key's
`ON DELETE` action: a row with no independent meaning without its parent cascades; a row
another user can reach, or that is training data, never cascades.

| FK | Action | Why |
|---|---|---|
| `sessions.user_id → users` | CASCADE | An auth artifact, not content. |
| `note_types.owner_id → users` | RESTRICT | User delete is not a supported operation (below). |
| `fields.note_type_id → note_types` | CASCADE | A field definition has no meaning without its note type. |
| `templates.note_type_id → note_types` | CASCADE | Same. |
| `decks.owner_id → users` | RESTRICT | See below. |
| `deck_access.deck_id → decks` | CASCADE | A grant on a deleted deck grants nothing. |
| `deck_access.user_id → users` | RESTRICT | See below. |
| `notes.owner_id → users` | RESTRICT | See below. |
| `notes.note_type_id → note_types` | RESTRICT | A note type cannot be deleted while notes reference it. |
| `notes.deck_id → decks` | RESTRICT | Deliberate tripwire — deck deletion goes through the transaction below, which clears these references first. |
| `notes.(deck_id, owner_id) → decks (id, owner_id)` | RESTRICT — `ON UPDATE RESTRICT ON DELETE RESTRICT` | Composite FK (#54, migration `00015`) enforcing `notes.owner_id = decks.owner_id`; see Identifiers, above. `decks.owner_id` is never updated (no ownership transfer), so `ON UPDATE` never fires in practice. |
| `cards.note_id → notes` | CASCADE | Cards are generated from a note. |
| `cards.template_id → templates` | RESTRICT | Never fires spuriously; kept as an assertion. |
| `cards.deck_id → decks` | CASCADE | Deleting a deck deletes its cards (architecture.md §20). |
| `user_card_state.user_id → users` | RESTRICT | See below. |
| `user_card_state.card_id → cards` | CASCADE | Scheduling state for a gone card is meaningless. |
| `review_log.user_id → users` | RESTRICT — permanent | A review belongs to a live user (CLAUDE.md §2.5). |
| `review_log.card_id → cards` | **no FK** | See below. |
| `user_fsrs_params.user_id → users` | RESTRICT | See below. |
| `user_fsrs_params.deck_id → decks` | CASCADE | A per-deck override for a deleted deck is dead configuration; the global row (`deck_id IS NULL`) is untouched. |
| `media_refs.deck_id → decks` | CASCADE | `media_refs` is deck-scoped by its own PK. |
| `media_refs.sha256 → media_blobs` | RESTRICT | A referenced blob is never deletable (blob GC deferred, see Media). |

**User deletion is not a supported operation in Phase 1.** No route deletes a user, and eight
FKs restrict on `users` so it is impossible rather than silently wrong: cascading
`decks.owner_id`, `notes.owner_id`, or `note_types.owner_id` would evaporate a shared or
co-authored resource for every other user holding a `deck_access` row the moment its creator
closes their account. `review_log.user_id` blocks permanently — a user who has ever answered a
card can never be deleted, which is CLAUDE.md §2.5 expressed as a constraint. The only user
delete the schema permits is a signup that never studied: `sessions.user_id` cascades, so
`DELETE FROM users` succeeds for an account with no decks, notes, access grants, scheduling
state, or reviews. Account closure needs an ownership-transfer decision for shared decks and a
written `review_log` decision (anonymise vs. delete) before it can be built — tracked in a
follow-up issue, not designed here.

**`review_log.card_id` has no foreign key, by decision.** RESTRICT there would make any deck
that had ever been studied permanently undeletable, colliding with architecture.md §20
("deleting a deck deletes its cards"). Dropping the FK is strictly stronger than RESTRICT
against the invariant it protects: no delete anywhere in this schema can remove a `review_log`
row, and none can be made to. `card_id` stays `NOT NULL` and a sound grouping key — UUIDv7 ids
are never reused — so history for a deleted card remains replayable. This is also Anki's own
shape: `revlog.cid` is not a foreign key there either (CLAUDE.md §2.10).

**Deck delete is an ordered transaction (`internal/db/deletion.go`), not a cascade.** Deleting
deck D deletes every card whose `deck_id` is D; a note is deleted only when it has no cards
left anywhere, not merely because D was its home deck — a note whose cards span decks survives
and is re-homed to the deck of its lowest-ordinal surviving card. No static FK expresses "delete
the note when its last card goes"; that is a predicate over sibling rows, not a per-reference
rewrite. The four steps, in one transaction:

1. Lock and authorise the deck row (`FOR UPDATE OF d`, joined against the caller's
   `deck_access`); no row → `pgx.ErrNoRows` → 404.
2. Delete every note this delete would leave with no cards at all.
3. Re-home every note still homed in D — by step 2's complement, each provably has a surviving
   card elsewhere.
4. `DELETE FROM decks`, which cascades to `cards`, `deck_access`, per-deck
   `user_fsrs_params`, and `media_refs`.

The post-condition — no note is ever left with zero cards — holds because step 4's cascade can
only delete cards in D, and every note whose cards were *all* in D was already removed in
step 2.

**Orphaned `media_blobs` cleanup is deferred.** A deck delete cascades its `media_refs` rows
away and can leave a `media_blobs` row with zero remaining references; nothing collects it —
see Media, below.

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
                 -- user_id is ON DELETE RESTRICT permanently; card_id has NO FK (#51) -- see Deletion policy.
                 -- Either way nothing ever cascades training data away, and now nothing can.

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

**Every foreign key carries its terminal `ON DELETE` action** — settled in
[#51](https://github.com/Jolls/enshu/issues/51); the full table and reasoning are in
Deletion policy, above.

**Indexes backing `ON DELETE RESTRICT`.** A restricting foreign key makes Postgres run
`SELECT 1 FROM child WHERE fk = $1` on every parent delete, so the referencing column needs
to lead an index or that becomes a sequential scan. `cards.template_id` and
`notes.note_type_id` are indexed for this. `review_log`'s `(card_id, user_id, reviewed_at)`
index no longer backs an RI check — `card_id` has no FK — and exists purely for the per-card
replay path. The `CASCADE` FKs need the same index support and already have it:
`cards.deck_id`, `cards.note_id` (via `UNIQUE (note_id, ordinal)`), `user_card_state.card_id`,
`deck_access.deck_id` (via its PK), `user_fsrs_params.deck_id`, `media_refs.deck_id` (via its PK),
and `fields`/`templates` on `note_type_id`. No new index is required by this change.
`deck_access.user_id` is `RESTRICT`, not `CASCADE` — see the Deletion policy table above — and
is backed by `deck_access_user_id_idx`.

**`stability` and `difficulty` are `double precision`, not `real`.** FSRS rounds them to
8 decimal places and clamps stability to 36500; `real` holds ~7 significant digits, so a
value round-tripped through it would not byte-match what the batch-preview and grade-time
`Repeat()` calls compute in memory (architecture.md §6, CLAUDE.md §10.2) — the consistency
check between them needs exact values to compare, not ones already degraded by storage.

`user_card_state.learning_steps` mirrors `go-fsrs`'s `Card.RemainingSteps` (v4's field name; the
column name follows the concept, not the library's current identifier). FSRS-6 short-term
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

A deck delete cascades its `media_refs` rows away and can leave a `media_blobs` row with zero
remaining references. Nothing collects it yet — a reference-counted GC needs to delete a row
and a file with no transaction spanning them, which is a separate feature blocked on #60. See
the follow-up issue filed alongside #51.

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

**Access-management operations require `can_view` alongside `can_manage_access`.** Reading the
access page (`GetDeckForAccessManage`), granting a new collaborator (`GrantDeckAccess`), and
locking the deck for a revoke or flag edit (`LockDeckForAccessChange`, `internal/db/queries/`)
all query for both flags together, never `can_manage_access` alone. Otherwise a caller whose
`can_view` was stripped but who still holds `can_manage_access` would be 404'd off the access
page yet could still grant, revoke, or edit other collaborators' rows by posting to the mutation
routes directly ([#83](https://github.com/Jolls/enshu/issues/83)).

---

## Migrations

Committed SQL, **immutable once merged**. Fix forward with a new migration; never edit an
applied one. Migration tool is `goose`, one migration per table, sequentially numbered
(`00001_users.sql`, `00002_sessions.sql`, …) via `goose -dir migrations create -s <name> sql` —
the `-s` flag is what forces sequential rather than timestamp-based numbering, which matters
because `sqlc` reads `migrations/` in filename order and 14 migrations authored in one sitting
would otherwise sort by accident rather than by intent. Apply with
`goose -dir migrations postgres "$DATABASE_URL" up`.

Checklist for a new table:

1. Write the migration; commit the SQL unedited. **One exception, before merge only:** a
   `NOT NULL` column added to an existing table. A bare `ADD COLUMN ... NOT NULL` applies fine
   to an empty database and fails on every database that has rows. Split it into
   add-nullable → backfill → `SET NOT NULL` by hand and cover it with a test that migrates a
   *populated* database, since the fresh-database test cannot see the failure.
2. Verify it applies cleanly to a fresh database.
3. Update this file's table listing and, if it holds per-user state, confirm the
   `(user_id, …)` key and index shape.

FK actions are part of the table definition, so a change to a deletion policy after a
migration has merged is an `ALTER TABLE … DROP CONSTRAINT … ADD CONSTRAINT …` migration, not an
edit to the original.
