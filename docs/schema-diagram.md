# Schema diagram

Visual companion to [schema.md](schema.md), which is the source of truth for rationale and
column-level detail. Regenerate this by hand if the shape in `schema.md` changes.

- [Full schema](#full-schema)
- [Content & authoring core](#content--authoring-core) — note types, fields, templates,
  notes, cards
- [Per-user scheduling](#per-user-scheduling) — the FSRS-critical subset
- [Access & sharing](#access--sharing) — users, decks, deck access
- [Media & auth](#media--auth) — content-addressed media, sessions

---

## Full schema

```mermaid
erDiagram
    USERS ||--o{ SESSIONS : "has"
    USERS ||--o{ DECKS : "owns"
    USERS ||--o{ NOTE_TYPES : "owns"
    USERS ||--o{ DECK_ACCESS : "granted"
    USERS ||--o{ USER_CARD_STATE : "has"
    USERS ||--o{ REVIEW_LOG : "generates"
    USERS ||--o{ USER_FSRS_PARAMS : "has"

    DECKS ||--o{ DECK_ACCESS : "shared via"
    DECKS ||--o{ NOTES : "contains"
    DECKS ||--o{ CARDS : "contains"
    DECKS ||--o{ MEDIA_REFS : "references"
    DECKS ||--o{ USER_FSRS_PARAMS : "scoped by (optional)"

    NOTE_TYPES ||--o{ FIELDS : "defines"
    NOTE_TYPES ||--o{ TEMPLATES : "defines"
    NOTE_TYPES ||--o{ NOTES : "typed by"

    NOTES ||--o{ CARDS : "generates"
    TEMPLATES ||--o{ CARDS : "renders"

    CARDS ||--o| USER_CARD_STATE : "scheduling state"
    CARDS ||--o{ REVIEW_LOG : "reviewed as"

    MEDIA_BLOBS ||--o{ MEDIA_REFS : "stored as"

    USERS {
        uuid id PK
        text email
        text password_hash
        text display_name
        text timezone
        smallint day_start_hour
        timestamptz created_at
    }

    SESSIONS {
        text id PK "sha256 of token"
        uuid user_id FK
        timestamptz expires_at
        timestamptz created_at
    }

    NOTE_TYPES {
        uuid id PK
        uuid owner_id FK
        text name "UNIQUE (owner_id, name)"
        text css
        bool is_cloze
        int sort_field_idx
        bigint anki_id "export fidelity only"
    }

    FIELDS {
        uuid id PK
        uuid note_type_id FK
        int ordinal
        text name
        text font
        int size
        bool is_rtl
        bool sticky
    }

    TEMPLATES {
        uuid id PK
        uuid note_type_id FK
        int ordinal
        text name
        text qfmt
        text afmt
        text browser_qfmt
        text browser_afmt
    }

    NOTES {
        uuid id PK
        text guid "UNIQUE (owner_id, guid)"
        uuid owner_id FK "denormalised from decks.owner_id"
        uuid note_type_id FK
        uuid deck_id FK
        jsonb fields
        text_array tags
        bigint checksum
        timestamptz created_at
        timestamptz modified_at
        bigint anki_id
    }

    CARDS {
        uuid id PK
        uuid note_id FK
        uuid template_id FK
        int ordinal "UNIQUE (note_id, ordinal)"
        uuid deck_id FK
        bigint anki_id
    }

    DECKS {
        uuid id PK
        uuid owner_id FK
        text name "UNIQUE (owner_id, name)"
        text description
        jsonb preset
        timestamptz created_at
        timestamptz modified_at
        bigint anki_id "export fidelity only"
    }

    DECK_ACCESS {
        uuid deck_id PK,FK
        uuid user_id PK,FK
        bool can_view
        bool can_study
        bool can_edit_content
        bool can_edit_settings
        bool can_manage_access
        bool can_delete
        timestamptz created_at
    }

    USER_CARD_STATE {
        uuid user_id PK,FK
        uuid card_id PK,FK
        timestamptz due
        float8 stability
        float8 difficulty
        smallint state "0 new,1 learning,2 review,3 relearning"
        int reps
        int lapses
        int elapsed_days
        int scheduled_days
        smallint learning_steps
        timestamptz last_review
        bool suspended
        date buried_until
        smallint flag
    }

    REVIEW_LOG {
        uuid id PK "client-generated UUIDv7"
        uuid user_id FK
        uuid card_id FK
        smallint rating "1-4"
        timestamptz reviewed_at
        int duration_ms
        smallint state_before
        smallint learning_steps_before
        float8 stability_before "NULL if imported"
        float8 difficulty_before "NULL if imported"
        int elapsed_days_before
        int scheduled_days_after
        smallint fsrs_version "NULL if imported"
        smallint review_kind
        bigint anki_id "revlog.id; UNIQUE (user_id, card_id, anki_id)"
    }

    USER_FSRS_PARAMS {
        uuid id PK "surrogate; real key is (user_id, deck_id)"
        uuid user_id FK
        uuid deck_id FK "NULL = global default"
        smallint fsrs_version
        jsonb params
        float8 desired_retention
        timestamptz optimised_at
        int review_count_at_fit
    }

    MEDIA_BLOBS {
        text sha256 PK
        bigint size_bytes
        text mime
        timestamptz created_at
    }

    MEDIA_REFS {
        uuid deck_id PK,FK
        text filename PK
        text sha256 FK
    }
```

**Reading this diagram:**

- No edge runs from `CARDS` to any scheduling column — that separation (content vs.
  `USER_CARD_STATE`) is the invariant the whole schema protects (CLAUDE.md §2.1).
- `CARDS ||--o|  USER_CARD_STATE` is one-per-`(user, card)` pair, not one-per-card; the
  diagram can't express the composite key directly, see `schema.md` for the real PK.
- The uniqueness annotations are the re-import dedup keys. Decks and note types key on
  **name**, the way Anki's own importer does — `anki_id` is per-collection (deck id 1 is
  `Default` everywhere), so keying on it merges unrelated collections. `review_log` is the one
  exception, since `revlog.id` genuinely identifies a row within its collection. See
  `schema.md`.
- `DECK_ACCESS` is the only path by which a deck reaches a second user. There is no visibility
  flag and no public-deck carve-out (CLAUDE.md §9), so every cross-user edge in this diagram
  passes through that table.

---

## Content & authoring core

`note_types` → `fields` + `templates`, and how a note type turns one `notes` row into N
`cards` rows. No users, no scheduling — this is what a deck's *content* is, independent of
who owns it or who's studying it.

```mermaid
erDiagram
    NOTE_TYPES ||--o{ FIELDS : "defines"
    NOTE_TYPES ||--o{ TEMPLATES : "defines"
    NOTE_TYPES ||--o{ NOTES : "typed by"
    NOTES ||--o{ CARDS : "generates"
    TEMPLATES ||--o{ CARDS : "renders"

    NOTE_TYPES {
        uuid id PK
        uuid owner_id FK
        text name "UNIQUE (owner_id, name)"
        text css
        bool is_cloze
        int sort_field_idx
        bigint anki_id "export fidelity only"
    }

    FIELDS {
        uuid id PK
        uuid note_type_id FK
        int ordinal
        text name
        text font
        int size
        bool is_rtl
        bool sticky
    }

    TEMPLATES {
        uuid id PK
        uuid note_type_id FK
        int ordinal
        text name
        text qfmt
        text afmt
        text browser_qfmt
        text browser_afmt
    }

    NOTES {
        uuid id PK
        text guid "UNIQUE (owner_id, guid)"
        uuid owner_id FK "denormalised from decks.owner_id"
        uuid note_type_id FK
        uuid deck_id FK
        jsonb fields
        text_array tags
        bigint checksum
        timestamptz created_at
        timestamptz modified_at
        bigint anki_id
    }

    CARDS {
        uuid id PK
        uuid note_id FK
        uuid template_id FK
        int ordinal "UNIQUE (note_id, ordinal)"
        uuid deck_id FK
        bigint anki_id
    }
```

One note type's fields feed both the note (values) and the templates (rendering rules);
`CARDS` is the join of a note with the template ordinal that generated it — cloze note types
are where "N cards per note" stops being 1:1.

---

## Per-user scheduling

`user_card_state`, `review_log`, and `user_fsrs_params` — the tables the FSRS wrapper reads
and writes, and the ones invariant §2.1 exists to protect. `CARDS` appears only as the
anchor they key off of, stripped of its content columns.

```mermaid
erDiagram
    USERS ||--o{ USER_CARD_STATE : "has"
    USERS ||--o{ REVIEW_LOG : "generates"
    USERS ||--o{ USER_FSRS_PARAMS : "has"
    CARDS ||--o| USER_CARD_STATE : "scheduling state"
    CARDS ||--o{ REVIEW_LOG : "reviewed as"

    CARDS {
        uuid id PK
        uuid note_id FK
        uuid template_id FK
        uuid deck_id FK
    }

    USER_CARD_STATE {
        uuid user_id PK,FK
        uuid card_id PK,FK
        timestamptz due
        float8 stability
        float8 difficulty
        smallint state "0 new,1 learning,2 review,3 relearning"
        int reps
        int lapses
        int elapsed_days
        int scheduled_days
        smallint learning_steps
        timestamptz last_review
        bool suspended
        date buried_until
        smallint flag
    }

    REVIEW_LOG {
        uuid id PK "client-generated UUIDv7"
        uuid user_id FK
        uuid card_id FK
        smallint rating "1-4"
        timestamptz reviewed_at
        int duration_ms
        smallint state_before
        smallint learning_steps_before
        float8 stability_before "NULL if imported"
        float8 difficulty_before "NULL if imported"
        int elapsed_days_before
        int scheduled_days_after
        smallint fsrs_version "NULL if imported"
        smallint review_kind
        bigint anki_id "revlog.id; UNIQUE (user_id, card_id, anki_id)"
    }

    USER_FSRS_PARAMS {
        uuid id PK "surrogate; real key is (user_id, deck_id)"
        uuid user_id FK
        uuid deck_id FK "NULL = global default"
        smallint fsrs_version
        jsonb params
        float8 desired_retention
        timestamptz optimised_at
        int review_count_at_fit
    }
```

`CARDS ||--o| USER_CARD_STATE` is drawn one-per-card because Mermaid can't express the real
composite key; the actual PK is `(user_id, card_id)`, i.e. one row per *pairing*, not one per
card. `USER_FSRS_PARAMS` doesn't reference `CARDS` at all — it's scoped to `(user_id,
deck_id)`, which is why it's absent from the edges above.

---

## Access & sharing

`deck_access` as the join table between users and decks — the only way a deck reaches someone
who doesn't own it. Small, but the shape Milestone 2's deck sharing and Milestone 3's classroom
cohorts build on.

```mermaid
erDiagram
    USERS ||--o{ DECKS : "owns"
    USERS ||--o{ DECK_ACCESS : "granted"
    DECKS ||--o{ DECK_ACCESS : "shared via"

    USERS {
        uuid id PK
        text email
        text display_name
    }

    DECKS {
        uuid id PK
        uuid owner_id FK
        text name
        jsonb preset
    }

    DECK_ACCESS {
        uuid deck_id PK,FK
        uuid user_id PK,FK
        bool can_view
        bool can_study
        bool can_edit_content
        bool can_edit_settings
        bool can_manage_access
        bool can_delete
        timestamptz created_at
    }
```

A user reaches a deck they don't own through `DECK_ACCESS` and nothing else. Their progress on
it still lives in their own `user_card_state` rows, which key off `card_id` and appear in the
scheduling diagram rather than this one — sharing content never shares progress.

---

## Media & auth

Two small, self-contained subsystems that don't interact with each other or with the rest of
the schema beyond a foreign key each — grouped together because neither earns a full diagram
alone.

```mermaid
erDiagram
    DECKS ||--o{ MEDIA_REFS : "references"
    MEDIA_BLOBS ||--o{ MEDIA_REFS : "stored as"
    USERS ||--o{ SESSIONS : "has"

    DECKS {
        uuid id PK
        text name
    }

    MEDIA_BLOBS {
        text sha256 PK
        bigint size_bytes
        text mime
        timestamptz created_at
    }

    MEDIA_REFS {
        uuid deck_id PK,FK
        text filename PK
        text sha256 FK
    }

    USERS {
        uuid id PK
        text email
    }

    SESSIONS {
        text id PK "sha256 of token"
        uuid user_id FK
        timestamptz expires_at
        timestamptz created_at
    }
```

Media is content-addressed and deduplicated across *all* decks, not just one owner's — two
decks shipping an identical image share one `MEDIA_BLOBS` row. Sessions store only the
token's hash; the raw token lives in the cookie and never touches the database.
