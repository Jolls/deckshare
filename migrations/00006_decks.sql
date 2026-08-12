-- +goose Up
CREATE TABLE decks (
    id          uuid        PRIMARY KEY DEFAULT uuidv7(),
    owner_id    uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,  -- #51: NOT a
                            -- stopgap. Cascading here would evaporate a shared deck for every
                            -- user holding a deck_access row when its creator closes their
                            -- account. Account deletion needs an ownership-transfer decision
                            -- that does not exist yet (§0.3).
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
