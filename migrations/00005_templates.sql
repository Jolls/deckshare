-- +goose Up
CREATE TABLE templates (
    id           uuid    PRIMARY KEY DEFAULT uuidv7(),
    note_type_id uuid    NOT NULL REFERENCES note_types (id) ON DELETE CASCADE,  -- #51: as fields
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
