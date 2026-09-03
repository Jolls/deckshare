# Plan: #50 — implement docs/schema.md in full

Phase 1, step 2 (architecture.md §11). Ships the complete DDL as `goose` migrations plus `sqlc`-generated Go. No handlers, no business logic.

## 0. Resolved decisions (no judgment calls left downstream)

**0.1 UUIDv7 mechanism — bump `compose.yaml` to `postgres:18`; every `uuid` PK carries `DEFAULT uuidv7()` as a belt-and-braces safety net, alongside application-supplied ids as the primary path.**

`postgres:16` (the image `compose.yaml` currently pins) has no native `uuidv7()` — that landed in PG18. `postgres:16` itself was never a deliberate choice for this project: it was inherited unchanged from the original, now-deleted TypeScript scaffold, with no rationale recorded anywhere in architecture.md's settled-decisions log. Since the schema is greenfield (no data to migrate) and there's no reason to prefer 16, **`compose.yaml`'s `image:` moves to `postgres:18`** in this PR.

The alternative of no DB default at all was considered and rejected in favor of the safety net: a `DEFAULT gen_random_uuid()` (UUIDv4) would contradict docs/schema.md §Identifiers (time-ordered ids, indexed like a sequence), but PG18's native `uuidv7()` produces the same id shape the application generates, so a default using it is not a correctness risk — it only fires if an insert forgets to supply an id, and produces a valid, time-ordered id when it does.

So: **every `uuid` PK is `uuid PRIMARY KEY DEFAULT uuidv7()`.** The application still generates ids in Go (`uuid.NewV7()` from `github.com/google/uuid` ≥ v1.6.0) at insert time and supplies them explicitly wherever the id needs to be known before the row exists (e.g. `review_log.id`, which the client generates for `ON CONFLICT (id) DO NOTHING` idempotency, and any row a handler needs to reference before commit) — the DB default is purely a fallback for the few paths that don't need to know the id in advance, not a change to the "generated client-side where possible" mechanism docs/schema.md describes.

**#50 adds no Go dependency yet.** `sqlc` with `sql_package: pgx/v5` maps `uuid` to `pgtype.UUID`, which lives in the `pgx/v5` module already in `go.mod`. `github.com/google/uuid` is added by the first issue that writes a row (#52, auth), not here.

Minimum Postgres version for this schema: **18** (the `uuidv7()` default is what raises the floor from 13 — `NULLS NOT DISTINCT` alone would only have required 15).

**0.2 FK `ON DELETE` policy — `RESTRICT` everywhere, without exception.**
schema.md only specifies `review_log`'s FKs ("`ON DELETE RESTRICT`: nothing may cascade training data away"). Deck/user deletion cascade policy is [#51](https://github.com/Jolls/deckshare/issues/51)'s job, and #50 must not pre-empt it. The conservative default that keeps every delete *blocked* until #51 decides is `ON DELETE RESTRICT`, applied to all 17 foreign keys. Consequence, and it is intended: **no user and no deck can be deleted at all until #51 lands.** There is no delete route in Phase 1 yet, so nothing is blocked in practice.

Every FK is written with an explicit `ON DELETE RESTRICT` and a `-- #51 revisits this` comment, so #51's diff is a mechanical grep rather than an audit.

Note this also makes the card-regeneration trap in schema.md ("delete every `cards` row for the note, reinsert") fail loudly rather than silently dropping `user_card_state` — `user_card_state.card_id` restricts, so a naive edit handler errors instead of discarding progress.

**0.3 `user_fsrs_params` "UNIQUE (user_id, deck_id) with NULL treated as a value."**
Postgres `UNIQUE` treats NULLs as distinct, so a plain constraint would let a user hold unlimited global-default rows. Two indexes, and only two:

```sql
CREATE UNIQUE INDEX user_fsrs_params_user_id_deck_id_key
    ON user_fsrs_params (user_id, deck_id);
CREATE UNIQUE INDEX user_fsrs_params_user_id_global_key
    ON user_fsrs_params (user_id) WHERE deck_id IS NULL;
```

The first is deliberately **not** partial: it enforces the per-deck uniqueness *and* leads with `user_id`, so it also backs the `ON DELETE RESTRICT` RI check on user delete (a pair of partial indexes could not — the planner can't prove a bare `user_id = $1` lookup is covered by either predicate). The second closes the NULL hole.

**0.4 `review_log` UNIQUE (user_id, card_id, anki_id) — plain UNIQUE is correct as-is.**
Postgres's default `NULLS DISTINCT` is exactly the wanted semantics: rows the live reviewer writes carry `anki_id IS NULL` and therefore collide on nothing, while imported rows dedup on the triple. No `NULLS NOT DISTINCT`, no partial index, no sentinel. A comment in the migration says so, because "why isn't this `NULLS NOT DISTINCT`?" is the obvious reviewer question.

**0.5 Migration granularity — one migration per table, 14 files, sequentially numbered.**
Forward-fix granularity (CLAUDE.md §9: immutable once merged) is worth more than a single tidy file: a wrong column on one table gets a one-table fix-forward migration whose scope is obvious, and a partially-applied run leaves goose's `goose_db_version` table pointing at exactly which table landed. Files are created with `goose -dir migrations create -s <name> sql` (sequential, `00001_*.sql`) rather than goose's default timestamp form — 14 migrations authored in one sitting get near-identical timestamps that sort by accident rather than by intent, and `sqlc` reads `migrations/` in filename order. `migrations/README.md` gets the `-s` documented (§6).

**0.6 `sqlc` query files — write one real query per table, don't ship an empty queries dir.**
`sqlc generate` emits `models.go` from the schema regardless of whether any query exists, so an empty `internal/db/queries/` is *probably* fine — but "probably" isn't good enough for the step that has to leave `go build ./...` green, and an empty `Querier` interface proves nothing about type mapping. So #50 ships 14 query files, one per table, each holding a single by-primary-key `:one` lookup. These are genuinely needed later, exercise every generated model type end to end, and are the smallest thing that proves the schema is queryable. Real query authorship belongs to #52–#59.

**0.7 `user_card_state` DSR columns are `NOT NULL DEFAULT 0`, not nullable.**
The row is written wholesale from a `go-fsrs` `Card`, whose zero value for a new card is `0` for stability/difficulty/reps/lapses/elapsed/scheduled/learning_steps. `NOT NULL DEFAULT 0` maps 1:1 onto Go `float64`/`int32` with no `pgtype` wrappers and no nil-vs-zero mapping for #54 to get wrong. `last_review` is the deliberate exception: it stays nullable because architecture.md §6's concurrency guard (`last_review IS NULL OR last_review < $reviewedAt`) reads the NULL as "never reviewed", so it is load-bearing.

`review_log.stability_before` / `difficulty_before` / `fsrs_version` **are** nullable — schema.md mandates it ("NULL for imported history"). `duration_ms` is nullable too: an import with no recorded time must not be written as a 0 ms review, which would poison an optimiser fit.

**0.8 `notes.checksum` is `NOT NULL` with no default.** Anki's `csum` is derived from the first field; a `DEFAULT 0` would let an insert path that forgot to compute it write a wrong value forever. No default means it fails loudly at the first CRUD insert (#53).

**0.9 `fields`/`templates` ordinal indexes are non-unique.** `cards` gets `UNIQUE (note_id, ordinal)` because schema.md mandates it. `fields` and `templates` get plain `(note_type_id, ordinal)` indexes instead: a note-type field reorder swaps two ordinals, and a non-deferrable unique constraint makes the intermediate state of a two-statement swap illegal. The index still backs the FK's RI check, which is the reason it exists.

**0.10 Name collision with the existing hand-written file — must be handled.**
`sqlc` with `sql_package: pgx/v5` writes **`internal/db/db.go`** (the `DBTX` interface and `Queries` struct). That is the exact path of the existing hand-written `NewPool` file, which sqlc will overwrite. Before the first `sqlc generate`: rename `internal/db/db.go` → `internal/db/pool.go`, carrying the package doc comment and the `//go:generate sqlc generate -f ../../sqlc.yaml` directive with it. `NewPool` is unchanged; `cmd/enshu/main.go` and `internal/http/http.go` need no edit.

## 1. Files to create

`migrations/` (14 files, applied in this order — FK dependency order):

| # | File | Table |
|---|---|---|
| 1 | `00001_users.sql` | `users` |
| 2 | `00002_sessions.sql` | `sessions` |
| 3 | `00003_note_types.sql` | `note_types` |
| 4 | `00004_fields.sql` | `fields` |
| 5 | `00005_templates.sql` | `templates` |
| 6 | `00006_decks.sql` | `decks` |
| 7 | `00007_deck_access.sql` | `deck_access` |
| 8 | `00008_notes.sql` | `notes` |
| 9 | `00009_cards.sql` | `cards` |
| 10 | `00010_user_card_state.sql` | `user_card_state` |
| 11 | `00011_review_log.sql` | `review_log` |
| 12 | `00012_user_fsrs_params.sql` | `user_fsrs_params` |
| 13 | `00013_media_blobs.sql` | `media_blobs` |
| 14 | `00014_media_refs.sql` | `media_refs` |

`internal/db/queries/`: `users.sql`, `sessions.sql`, `note_types.sql`, `fields.sql`, `templates.sql`, `decks.sql`, `deck_access.sql`, `notes.sql`, `cards.sql`, `user_card_state.sql`, `review_log.sql`, `user_fsrs_params.sql`, `media_blobs.sql`, `media_refs.sql`.

Renamed: `internal/db/db.go` → `internal/db/pool.go` (§0.10).

Also edited: `compose.yaml` (postgres:16 → 18, §0.1), `.github/workflows/ci.yml` (Postgres service + migration/sqlc-diff steps, §7), `docs/schema.md`, `migrations/README.md`, `docs/architecture.md` §1/§3, `CHANGELOG.md` (§6).

Generated and committed (do not hand-edit): `internal/db/db.go`, `internal/db/models.go`, `internal/db/querier.go`, and `internal/db/<name>.sql.go` × 14.

Every `-- +goose Down` is a bare `DROP TABLE <name>;` — indexes and constraints drop with the table. No `IF EXISTS`; a down that can't find its table should fail.

## 2. Migration SQL

### 00001_users.sql
```sql
-- +goose Up
CREATE TABLE users (
    id             uuid        PRIMARY KEY DEFAULT uuidv7(),  -- application-supplied; DB default is a safety net
    email          text        NOT NULL,
    password_hash  text        NOT NULL,             -- argon2id only; never a weaker algorithm
    display_name   text        NOT NULL,
    timezone       text        NOT NULL DEFAULT 'UTC',   -- IANA name; drives the day boundary
    day_start_hour smallint    NOT NULL DEFAULT 4
        CONSTRAINT users_day_start_hour_check CHECK (day_start_hour BETWEEN 0 AND 23),
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- One account per address, any casing. This index is also what makes a racing duplicate
-- signup a clean 409 rather than two accounts (architecture.md §12).
CREATE UNIQUE INDEX users_email_lower_key ON users (lower(email));

-- +goose Down
DROP TABLE users;
```

### 00002_sessions.sql
```sql
-- +goose Up
CREATE TABLE sessions (
    id         text        PRIMARY KEY,   -- SHA-256 hex of the session token; the raw token
                                          -- lives only in the cookie, never in the database
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,  -- #51 revisits
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Invalidate all of a user's sessions; also backs the RESTRICT RI check on user delete.
CREATE INDEX sessions_user_id_idx ON sessions (user_id);

-- +goose Down
DROP TABLE sessions;
```

### 00003_note_types.sql
```sql
-- +goose Up
CREATE TABLE note_types (
    id             uuid     PRIMARY KEY DEFAULT uuidv7(),
    owner_id       uuid     NOT NULL REFERENCES users (id) ON DELETE RESTRICT,  -- #51 revisits
    name           text     NOT NULL,
    css            text     NOT NULL DEFAULT '',
    is_cloze       boolean  NOT NULL DEFAULT false,
    sort_field_idx integer  NOT NULL DEFAULT 0,
    anki_id        bigint,                 -- export fidelity only; never a key. NULL when authored here.

    -- Re-import reuses the owner's note type of that name. NOT (owner_id, anki_id):
    -- Anki note-type ids are per-profile, so keying on them merges unrelated note types
    -- and renders every field into the wrong slot (docs/schema.md §Identifiers).
    -- Leads with owner_id, so it also backs the RESTRICT RI check on user delete.
    CONSTRAINT note_types_owner_id_name_key UNIQUE (owner_id, name)
);

-- +goose Down
DROP TABLE note_types;
```

### 00004_fields.sql
```sql
-- +goose Up
CREATE TABLE fields (
    id           uuid    PRIMARY KEY DEFAULT uuidv7(),
    note_type_id uuid    NOT NULL REFERENCES note_types (id) ON DELETE RESTRICT,  -- #51 revisits
    ordinal      integer NOT NULL,        -- position; notes.fields is indexed by this
    name         text    NOT NULL,
    font         text    NOT NULL DEFAULT 'Arial',
    size         integer NOT NULL DEFAULT 20,
    is_rtl       boolean NOT NULL DEFAULT false,
    sticky       boolean NOT NULL DEFAULT false
);

-- Non-unique on purpose: reordering a note type's fields swaps two ordinals, and a
-- non-deferrable UNIQUE would make the intermediate state of that swap illegal.
-- Leads with note_type_id, so it backs the RESTRICT RI check.
CREATE INDEX fields_note_type_id_ordinal_idx ON fields (note_type_id, ordinal);

-- +goose Down
DROP TABLE fields;
```

### 00005_templates.sql
```sql
-- +goose Up
CREATE TABLE templates (
    id           uuid    PRIMARY KEY DEFAULT uuidv7(),
    note_type_id uuid    NOT NULL REFERENCES note_types (id) ON DELETE RESTRICT,  -- #51 revisits
    ordinal      integer NOT NULL,
    name         text    NOT NULL,
    qfmt         text    NOT NULL,
    afmt         text    NOT NULL,
    browser_qfmt text    NOT NULL DEFAULT '',   -- Anki stores '' for "use qfmt/afmt"
    browser_afmt text    NOT NULL DEFAULT ''
);

CREATE INDEX templates_note_type_id_ordinal_idx ON templates (note_type_id, ordinal);

-- +goose Down
DROP TABLE templates;
```

### 00006_decks.sql
```sql
-- +goose Up
CREATE TABLE decks (
    id          uuid        PRIMARY KEY DEFAULT uuidv7(),
    owner_id    uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,  -- #51 revisits
    name        text        NOT NULL,       -- full path, Anki-style ("Parent::Child")
    description text        NOT NULL DEFAULT '',
    preset      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    modified_at timestamptz NOT NULL DEFAULT now(),
    anki_id     bigint,                     -- export fidelity only; never a key

    -- Re-import matches an updated export to the deck of that name. NOT (owner_id, anki_id):
    -- deck id 1 is "Default" in every collection that has ever existed.
    CONSTRAINT decks_owner_id_name_key UNIQUE (owner_id, name)
);

-- +goose Down
DROP TABLE decks;
```

### 00007_deck_access.sql
```sql
-- +goose Up
-- The ONLY thing that makes a deck reachable by a second user. Six independent permissions
-- per (user, deck) -- no role enum, no implied hierarchy. can_view being a practical
-- prerequisite for the other five is an application-level convention, deliberately not
-- enforced here (docs/schema.md).
CREATE TABLE deck_access (
    deck_id           uuid        NOT NULL REFERENCES decks (id) ON DELETE RESTRICT,  -- #51 revisits
    user_id           uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,  -- #51 revisits
    can_view          boolean     NOT NULL DEFAULT false,
    can_study         boolean     NOT NULL DEFAULT false,
    can_edit_content  boolean     NOT NULL DEFAULT false,
    can_edit_settings boolean     NOT NULL DEFAULT false,
    can_manage_access boolean     NOT NULL DEFAULT false,
    can_delete        boolean     NOT NULL DEFAULT false,
    created_at        timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (deck_id, user_id)
);

-- "Decks shared with me", and the RESTRICT RI check on user delete (the PK covers deck_id).
CREATE INDEX deck_access_user_id_idx ON deck_access (user_id);

-- +goose Down
DROP TABLE deck_access;
```

### 00008_notes.sql
```sql
-- +goose Up
CREATE TABLE notes (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    guid         text        NOT NULL,   -- Anki's stable per-note id; the import idempotency key
    owner_id     uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,       -- #51 revisits
    note_type_id uuid        NOT NULL REFERENCES note_types (id) ON DELETE RESTRICT,  -- #51 revisits
    deck_id      uuid        NOT NULL REFERENCES decks (id) ON DELETE RESTRICT,       -- #51 revisits
    fields       jsonb       NOT NULL DEFAULT '[]'::jsonb,  -- ordered array of strings, indexed by fields.ordinal
    tags         text[]      NOT NULL DEFAULT '{}',
    checksum     bigint      NOT NULL,   -- Anki csum; no default, so a path that forgets to
                                         -- compute it fails loudly instead of writing 0
    created_at   timestamptz NOT NULL DEFAULT now(),
    modified_at  timestamptz NOT NULL DEFAULT now(),
    anki_id      bigint,

    -- Makes re-import idempotent. owner_id is denormalised from decks.owner_id because a
    -- unique index can't span a join; moving a note between decks must set it to the new
    -- deck's owner. Nothing enforces that equality at the DB level -- by design, per
    -- docs/schema.md. No trigger.
    CONSTRAINT notes_owner_id_guid_key UNIQUE (owner_id, guid)
);

CREATE INDEX notes_deck_id_idx      ON notes (deck_id);        -- deck-scoped queries + deck-delete RI
CREATE INDEX notes_note_type_id_idx ON notes (note_type_id);   -- RESTRICT RI check

-- +goose Down
DROP TABLE notes;
```

### 00009_cards.sql
```sql
-- +goose Up
-- Content addressing ONLY. No due, no ivl, no factor, no state -- scheduling lives in
-- user_card_state, keyed (user_id, card_id). This is the invariant the schema exists to
-- protect (CLAUDE.md §2.1).
CREATE TABLE cards (
    id          uuid    PRIMARY KEY DEFAULT uuidv7(),
    note_id     uuid    NOT NULL REFERENCES notes (id) ON DELETE RESTRICT,      -- #51 revisits
    template_id uuid    NOT NULL REFERENCES templates (id) ON DELETE RESTRICT,  -- #51 revisits
    ordinal     integer NOT NULL,   -- template ordinal, or cloze ordinal for cloze note types
    deck_id     uuid    NOT NULL REFERENCES decks (id) ON DELETE RESTRICT,      -- #51 revisits
    anki_id     bigint,

    CONSTRAINT cards_note_id_ordinal_key UNIQUE (note_id, ordinal)   -- also backs note-delete RI
);

CREATE INDEX cards_template_id_idx ON cards (template_id);   -- RESTRICT RI check (docs/schema.md)
CREATE INDEX cards_deck_id_idx     ON cards (deck_id);       -- deck-scoped queries + deck-delete RI

-- +goose Down
DROP TABLE cards;
```

### 00010_user_card_state.sql
```sql
-- +goose Up
-- Per-user scheduling state. One row per (user, card) pairing, never per card.
CREATE TABLE user_card_state (
    user_id        uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,  -- #51 revisits
    card_id        uuid        NOT NULL REFERENCES cards (id) ON DELETE RESTRICT,  -- #51 revisits
    due            timestamptz NOT NULL,
    -- double precision, not real: FSRS rounds to 8dp and clamps stability to 36500, and the
    -- batch-preview/grade-time consistency check (CLAUDE.md §10.2) compares exact values.
    -- NOT NULL DEFAULT 0 mirrors go-fsrs's own zero value for a new card, so there is no
    -- nil-vs-zero mapping for internal/fsrs to get wrong.
    stability      double precision NOT NULL DEFAULT 0,
    difficulty     double precision NOT NULL DEFAULT 0,
    state          smallint    NOT NULL DEFAULT 0    -- FSRS State: 0 new, 1 learning, 2 review, 3 relearning
        CONSTRAINT user_card_state_state_check CHECK (state BETWEEN 0 AND 3),
    reps           integer     NOT NULL DEFAULT 0,
    lapses         integer     NOT NULL DEFAULT 0,
    elapsed_days   integer     NOT NULL DEFAULT 0,
    scheduled_days integer     NOT NULL DEFAULT 0,
    learning_steps smallint    NOT NULL DEFAULT 0,   -- mirrors go-fsrs Card.LearningSteps; FSRS-6
                                                     -- short-term scheduling reads it
    last_review    timestamptz,                      -- NULL = never reviewed. Load-bearing: the
                                                     -- last-write-wins-by-review-time guard reads it
    suspended      boolean     NOT NULL DEFAULT false,
    buried_until   date,
    flag           smallint    NOT NULL DEFAULT 0,

    PRIMARY KEY (user_id, card_id)
);

-- The queue query.
CREATE INDEX user_card_state_user_id_due_idx
    ON user_card_state (user_id, due) WHERE NOT suspended;

-- RESTRICT RI check on card delete (the PK leads with user_id, so it can't serve this).
CREATE INDEX user_card_state_card_id_idx ON user_card_state (card_id);

-- +goose Down
DROP TABLE user_card_state;
```

### 00011_review_log.sql
```sql
-- +goose Up
-- APPEND-ONLY. This is the optimiser's training set, not bookkeeping. No DELETE path without
-- a written decision (docs/schema.md, CLAUDE.md §2.5). The FKs below are ON DELETE RESTRICT
-- for that reason and that reason alone -- unlike every other RESTRICT in this schema, these
-- two are NOT provisional and #51 must not relax them.
CREATE TABLE review_log (
    id                    uuid        PRIMARY KEY DEFAULT uuidv7(),  -- client-generated UUIDv7;
                                                    -- makes retry idempotent via
                                                    -- ON CONFLICT (id) DO NOTHING. The DB default
                                                    -- is a safety net only -- the client always
                                                    -- supplies this one explicitly. Every OTHER
                                                    -- column is computed server-side.
    user_id               uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    card_id               uuid        NOT NULL REFERENCES cards (id) ON DELETE RESTRICT,
    rating                smallint    NOT NULL
        CONSTRAINT review_log_rating_check CHECK (rating BETWEEN 1 AND 4),
    reviewed_at           timestamptz NOT NULL,
    duration_ms           integer,                  -- NULL when unknown (some imported history);
                                                    -- never 0 as a stand-in for "unknown"
    state_before          smallint    NOT NULL
        CONSTRAINT review_log_state_before_check CHECK (state_before BETWEEN 0 AND 3),
    learning_steps_before smallint    NOT NULL,
    stability_before      double precision,         -- NULL for imported history
    difficulty_before     double precision,         -- NULL for imported history
    elapsed_days_before   integer     NOT NULL,
    scheduled_days_after  integer     NOT NULL,
    fsrs_version          smallint,                 -- NULL for imported history; what keeps a row
                                                    -- replayable across a refit or upgrade
    review_kind           smallint    NOT NULL
        CONSTRAINT review_log_review_kind_check CHECK (review_kind BETWEEN 0 AND 4),
    anki_id               bigint,                   -- revlog.id; NULL for rows our reviewer produced

    -- Re-import dedup. Plain UNIQUE is correct: Postgres treats NULLs as distinct by default,
    -- which is exactly what's wanted -- rows the live reviewer writes have anki_id NULL and so
    -- collide on nothing. Do NOT "fix" this to NULLS NOT DISTINCT. user_id leads because one
    -- user's imported history must never block another's.
    CONSTRAINT review_log_user_id_card_id_anki_id_key UNIQUE (user_id, card_id, anki_id)
);

-- card_id leads for the RESTRICT RI check; the trailing columns serve the per-card replay path.
CREATE INDEX review_log_card_id_user_id_reviewed_at_idx
    ON review_log (card_id, user_id, reviewed_at);

CREATE INDEX review_log_user_id_reviewed_at_idx ON review_log (user_id, reviewed_at);

-- +goose Down
DROP TABLE review_log;
```

### 00012_user_fsrs_params.sql
```sql
-- +goose Up
-- Per-user, optionally per (user, deck). NEVER fit one parameter set across a cohort.
CREATE TABLE user_fsrs_params (
    id                  uuid     PRIMARY KEY DEFAULT uuidv7(),  -- surrogate; the real key is (user_id, deck_id)
    user_id             uuid     NOT NULL REFERENCES users (id) ON DELETE RESTRICT,  -- #51 revisits
    deck_id             uuid     REFERENCES decks (id) ON DELETE RESTRICT,           -- #51 revisits
                                                -- NULL = the user's global default
    fsrs_version        smallint NOT NULL,      -- explicit: 17 weights in 4.5, 19 in 5, 21 in 6
    params              jsonb    NOT NULL DEFAULT '[]'::jsonb,  -- JSON array, never fixed-width
                                                -- columns. Empty array = use the library defaults
                                                -- (the retention-override-only row, #59)
    desired_retention   double precision NOT NULL
        CONSTRAINT user_fsrs_params_desired_retention_check
            CHECK (desired_retention > 0 AND desired_retention < 1),
    optimised_at        timestamptz,            -- NULL until a fit has run
    review_count_at_fit integer                 -- NULL until a fit has run
);

-- "UNIQUE (user_id, deck_id) with NULL treated as a value", in two indexes. The first is
-- deliberately NOT partial: it enforces per-deck uniqueness and, leading with user_id, backs
-- the RESTRICT RI check on user delete, which a pair of partial indexes could not.
CREATE UNIQUE INDEX user_fsrs_params_user_id_deck_id_key
    ON user_fsrs_params (user_id, deck_id);
CREATE UNIQUE INDEX user_fsrs_params_user_id_global_key
    ON user_fsrs_params (user_id) WHERE deck_id IS NULL;

CREATE INDEX user_fsrs_params_deck_id_idx ON user_fsrs_params (deck_id);  -- deck-delete RI

-- +goose Down
DROP TABLE user_fsrs_params;
```

### 00013_media_blobs.sql
```sql
-- +goose Up
-- Metadata only. Bytes live on the filesystem at ${MEDIA_ROOT}/<sha[0:2]>/<sha> -- there is
-- never a bytea column here (architecture.md §12). Deduplicated across ALL users.
CREATE TABLE media_blobs (
    sha256      text        PRIMARY KEY,   -- lowercase hex
    size_bytes  bigint      NOT NULL,
    mime        text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE media_blobs;
```

### 00014_media_refs.sql
```sql
-- +goose Up
-- Anki references media by filename inside note fields (<img src="x.jpg">), so this mapping
-- is what rendering resolves against. A filename collision within one import is survivable:
-- first-seen wins, with a warning (docs/schema.md §Media) -- enforced by the PK below.
CREATE TABLE media_refs (
    deck_id  uuid NOT NULL REFERENCES decks (id) ON DELETE RESTRICT,          -- #51 revisits
    filename text NOT NULL,                                                   -- NFC-normalised on read
    sha256   text NOT NULL REFERENCES media_blobs (sha256) ON DELETE RESTRICT, -- #51 revisits

    PRIMARY KEY (deck_id, filename)
);

CREATE INDEX media_refs_sha256_idx ON media_refs (sha256);   -- blob-delete RI + reverse lookup

-- +goose Down
DROP TABLE media_refs;
```

## 3. sqlc query files

One file per table under `internal/db/queries/`, each a single by-PK lookup. Exact contents:

```sql
-- users.sql
-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- sessions.sql
-- name: GetSession :one
SELECT * FROM sessions WHERE id = $1;

-- note_types.sql
-- name: GetNoteType :one
SELECT * FROM note_types WHERE id = $1;

-- fields.sql
-- name: GetField :one
SELECT * FROM fields WHERE id = $1;

-- templates.sql
-- name: GetTemplate :one
SELECT * FROM templates WHERE id = $1;

-- decks.sql
-- name: GetDeck :one
SELECT * FROM decks WHERE id = $1;

-- deck_access.sql
-- name: GetDeckAccess :one
SELECT * FROM deck_access WHERE deck_id = $1 AND user_id = $2;

-- notes.sql
-- name: GetNote :one
SELECT * FROM notes WHERE id = $1;

-- cards.sql
-- name: GetCard :one
SELECT * FROM cards WHERE id = $1;

-- user_card_state.sql
-- name: GetUserCardState :one
SELECT * FROM user_card_state WHERE user_id = $1 AND card_id = $2;

-- review_log.sql
-- name: GetReviewLogEntry :one
SELECT * FROM review_log WHERE id = $1;

-- user_fsrs_params.sql
-- name: GetUserFsrsParams :one
SELECT * FROM user_fsrs_params WHERE id = $1;

-- media_blobs.sql
-- name: GetMediaBlob :one
SELECT * FROM media_blobs WHERE sha256 = $1;

-- media_refs.sql
-- name: GetMediaRef :one
SELECT * FROM media_refs WHERE deck_id = $1 AND filename = $2;
```

Deliberately **not** here, because they belong to the issues that need them: any query joining `deck_access` (#53+), the queue query (#57), the grading upsert (#57), import dedup upserts (#58). Note for whoever writes those: CLAUDE.md §9 — every deck-touching query takes a `user_id` and joins `deck_access`, and a deck the caller can't see must be indistinguishable from one that doesn't exist.

## 4. Implementation sequence

1. Rename `internal/db/db.go` → `internal/db/pool.go` (§0.10). Verify `go build ./...` still passes before touching anything else.
2. Author the 14 migration files with the exact SQL above.
3. Author the 14 query files.
4. `sqlc generate`, commit the generated output.
5. Docs (§6).

## 5. Verification

Dev database is `compose.yaml`'s `postgres:18` (bumped from `16`, §0.1) on `localhost:5432`, and `.env.example` already carries the matching `DATABASE_URL=postgres://root:mysecretpassword@localhost:5432/local`. `goose` and `sqlc` need to be available on PATH (`go install github.com/pressly/goose/v3/cmd/goose@latest` and `go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest`).

Against a **fresh** database (`docker compose down -v && docker compose up -d db`, per schema.md's migration checklist item 2):

```
goose -dir migrations postgres "$DATABASE_URL" up
goose -dir migrations postgres "$DATABASE_URL" status     # 14 applied, none pending
goose -dir migrations postgres "$DATABASE_URL" down-to 0  # every Down is reversible
goose -dir migrations postgres "$DATABASE_URL" up         # and re-applies clean
sqlc generate
go build ./... && go vet ./... && golangci-lint run && go test ./...
```

Spot-checks worth doing by hand once, because they're the constraints most likely to be silently wrong:

- Two `user_fsrs_params` rows for one user with `deck_id IS NULL` → must fail (§0.3).
- Two `review_log` rows differing only by a NULL `anki_id` → must both insert (§0.4).
- `INSERT INTO users (...) VALUES (..., 24, ...)` → must fail the `day_start_hour` check.
- `desired_retention = 1.0` → must fail (strictly-between; see open question 1).
- Delete a user who has a session → must fail with an FK violation (§0.2, intended).

schema.md's migration-checklist exception (split a `NOT NULL` add into add-nullable → backfill → `SET NOT NULL`) **does not apply to any column here** — every column arrives with its table, on an empty database, so there is nothing to backfill. Worth stating in the PR so a reviewer doesn't look for it.

**A Postgres service is added to CI in this PR** (resolved decision — see §7 below), so the checks above run on every push/PR, not just by hand.

## 6. Doc updates in the same PR

- **`docs/schema.md`** — the "Migrations" section still says *"Migration tool is not yet chosen — see architecture.md §12"*; that was settled (goose) in 0.1.5 and is now shipped. Replace with the goose commands and the sequential-numbering convention. Also add to §Identifiers: uuid PKs carry `DEFAULT uuidv7()` (PG18) as a safety net, application ids are `uuid.NewV7()` in Go and remain the primary path (§0.1). Add a short note under the FK discussion that every FK except `review_log`'s is provisionally `ON DELETE RESTRICT` pending #51 — and adjust the card-regeneration paragraph, which currently says a naive delete "cascade-deletes its `user_card_state` silently": under today's FKs it can't, it errors. The trap is still real once #51 introduces any cascade, so keep the warning, just re-tense it.
- **`migrations/README.md`** — line 18 (*"Schema §5 (docs/schema.md) lands in #50"*) becomes a statement of what's there. Document `goose -dir migrations create -s <name> sql` (the `-s` is the convention, §0.5) and that `sqlc` reads these files directly, so a migration that doesn't parse breaks codegen, not just `goose up`.
- **`docs/architecture.md` §1** — *"No schema, auth, FSRS, or `.apkg` logic yet"* is now wrong. Note build-order step 2 as landed in #50, leaving auth/FSRS/apkg as the remaining gaps. **§3's stack table** — the `desired_retention` validation guidance parenthetical currently reads "reject ... outside `(0, 1]`"; correct it to `(0, 1)` to match the strict DB CHECK (§0, resolved decision above) — the two docs disagreed and schema.md's stricter rule wins (retention 1.0 is not a meaningful target).
- **`compose.yaml`** — `image: postgres:16` → `image: postgres:18` (§0.1).
- **`.github/workflows/ci.yml`** — add the Postgres service and migration/sqlc-diff steps (§7).
- **`CHANGELOG.md`** — new version entry with an `### Added` entry: full schema as 14 goose migrations, `sqlc`-generated models/queries, CI Postgres service verifying migrations apply and generated code matches the schema; an `### Changed` entry: Postgres bumped to 18 (enables `DEFAULT uuidv7()`), the provisional blanket `ON DELETE RESTRICT` pending #51, and the `internal/db/db.go` → `pool.go` rename.
- **`docs/schema-diagram.md`** — no change needed; it already matches this DDL column for column. Re-read it after implementing to confirm nothing drifted.

## 7. CI: add a Postgres service (resolved decision)

`.github/workflows/ci.yml`'s single `build` job gets a `postgres:18` service container and two new steps, inserted after `go vet` and before the `golangci-lint-action`/`go test` steps (linting and unit tests don't need the DB, so they stay fast regardless of service-container startup time — but ordering doesn't gate correctness here, only readability of a failed run):

```yaml
jobs:
  build:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:18
        env:
          POSTGRES_USER: root
          POSTGRES_PASSWORD: mysecretpassword
          POSTGRES_DB: local
        ports:
          - 5432:5432
        options: >-
          --health-cmd pg_isready
          --health-interval 10s
          --health-timeout 5s
          --health-retries 5
    env:
      DATABASE_URL: postgres://root:mysecretpassword@localhost:5432/local
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26"
      - run: go build ./...
      - run: go vet ./...
      - name: Install goose and sqlc
        run: |
          go install github.com/pressly/goose/v3/cmd/goose@latest
          go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
      - name: Apply migrations to a fresh database
        run: goose -dir migrations postgres "$DATABASE_URL" up
      - name: sqlc generate must match committed output
        run: |
          sqlc generate
          git diff --exit-code -- internal/db
      - uses: golangci/golangci-lint-action@v7
        with:
          version: v2.12.2
      - run: go test ./...
```

The `git diff --exit-code` step is what makes "generated code is committed and must not be hand-edited" (CLAUDE.md §16) an enforced check rather than a convention: if a future PR edits `migrations/` and forgets to re-run `sqlc generate`, or edits `internal/db/*.sql.go` by hand, CI fails on the diff instead of silently drifting. `goose down-to 0` is deliberately **not** added to this job — it belongs in the manual verification checklist (§5) as a one-time author check, not a per-PR gate; running it in CI would double migration time on every push for a property (reversibility) that doesn't regress silently the way generated-code drift does.

This makes `compose.yaml` and CI agree on the same Postgres major version (18), so "works in CI" and "works against the dev compose stack" test the same thing.

## Open questions for the user

None remaining — all four were resolved in conversation before implementation:
- `desired_retention`: strict `< 1` (schema.md wins; architecture.md §3's parenthetical gets corrected in this PR's doc updates, §6).
- Blanket `ON DELETE RESTRICT`: shipping as planned, intentional stopgap until #51.
- Postgres version: bumped to `postgres:18` in `compose.yaml` (§0.1), enabling `DEFAULT uuidv7()` on every `uuid` PK.
- CI: Postgres service added now (§7), not deferred.
