-- +goose Up
CREATE TABLE note_types (
    id             uuid     PRIMARY KEY DEFAULT uuidv7(),
    owner_id       uuid     NOT NULL REFERENCES users (id) ON DELETE RESTRICT,  -- #51: user delete
                            -- is not a supported operation (docs/plans/51-deletion-policy.md §0.3)
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
