-- +goose Up
-- Per-user, optionally per (user, deck). NEVER fit one parameter set across a cohort.
CREATE TABLE user_fsrs_params (
    id                  uuid     PRIMARY KEY DEFAULT uuidv7(),  -- surrogate; the real key is (user_id, deck_id)
    user_id             uuid     NOT NULL REFERENCES users (id) ON DELETE RESTRICT,  -- #51: §0.3
    deck_id             uuid     REFERENCES decks (id) ON DELETE CASCADE,            -- #51: a
                                 -- per-deck override for a deleted deck is dead configuration.
                                 -- The user's global row (deck_id IS NULL) is never touched.
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
