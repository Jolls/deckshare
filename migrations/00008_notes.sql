-- +goose Up
CREATE TABLE notes (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    guid         text        NOT NULL,   -- Anki's stable per-note id; the import idempotency key
    owner_id     uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,       -- #51: §0.3
    note_type_id uuid        NOT NULL REFERENCES note_types (id) ON DELETE RESTRICT,  -- #51: a
                             -- note type cannot be deleted while notes are written with it
                             -- (routes.md's note-type delete rule, enforced where it can't be
                             -- bypassed)
    deck_id      uuid        NOT NULL REFERENCES decks (id) ON DELETE RESTRICT,       -- #51:
                             -- deliberate tripwire. Deck deletion goes through DeleteDeck
                             -- (internal/db/deletion.go), which deletes card-less notes and
                             -- re-homes the survivors BEFORE deleting the deck. A bare
                             -- DELETE FROM decks must fail here rather than take notes whose
                             -- cards live in other decks with it (architecture.md §20).
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

CREATE INDEX notes_deck_id_idx ON notes (deck_id);   -- deck-scoped queries + the deck-delete
                                                     -- RESTRICT check and DeleteDeck's re-home step
CREATE INDEX notes_note_type_id_idx ON notes (note_type_id);   -- RESTRICT RI check

-- +goose Down
DROP TABLE notes;
