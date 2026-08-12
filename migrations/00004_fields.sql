-- +goose Up
CREATE TABLE fields (
    id           uuid    PRIMARY KEY DEFAULT uuidv7(),
    note_type_id uuid    NOT NULL REFERENCES note_types (id) ON DELETE CASCADE,  -- #51: a field
                         -- definition has no meaning without its note type
    ordinal      integer NOT NULL,        -- position; notes.fields is indexed by this
    name         text    NOT NULL,
    font         text    NOT NULL DEFAULT 'Arial',
    size         integer NOT NULL DEFAULT 20,
    is_rtl       boolean NOT NULL DEFAULT false,
    sticky       boolean NOT NULL DEFAULT false
);

-- Non-unique on purpose: reordering a note type's fields swaps two ordinals, and a
-- non-deferrable UNIQUE would make the intermediate state of that swap illegal.
-- Leads with note_type_id, so it backs the CASCADE on note_type delete (#51).
CREATE INDEX fields_note_type_id_ordinal_idx ON fields (note_type_id, ordinal);

-- +goose Down
DROP TABLE fields;
