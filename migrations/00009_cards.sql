-- +goose Up
-- Content addressing ONLY. No due, no ivl, no factor, no state -- scheduling lives in
-- user_card_state, keyed (user_id, card_id). This is the invariant the schema exists to
-- protect (CLAUDE.md §2.1).
CREATE TABLE cards (
    id          uuid    PRIMARY KEY DEFAULT uuidv7(),
    note_id     uuid    NOT NULL REFERENCES notes (id) ON DELETE CASCADE,       -- #51: cards are
                        -- generated from their note and die with it
    template_id uuid    NOT NULL REFERENCES templates (id) ON DELETE RESTRICT,  -- #51: can only
                        -- fire vacuously (notes.note_type_id blocks first), kept as an assertion
    ordinal     integer NOT NULL,   -- template ordinal, or cloze ordinal for cloze note types
    deck_id     uuid    NOT NULL REFERENCES decks (id) ON DELETE CASCADE,       -- #51: deleting a
                        -- deck deletes its cards (architecture.md §20)
    anki_id     bigint,

    CONSTRAINT cards_note_id_ordinal_key UNIQUE (note_id, ordinal)   -- also backs note-delete RI
);

CREATE INDEX cards_template_id_idx ON cards (template_id);   -- RESTRICT RI check (docs/schema.md)
CREATE INDEX cards_deck_id_idx     ON cards (deck_id);       -- deck-scoped queries + deck-delete RI

-- +goose Down
DROP TABLE cards;
