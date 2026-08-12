-- +goose Up
-- Per-user scheduling state. One row per (user, card) pairing, never per card.
CREATE TABLE user_card_state (
    user_id        uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,  -- #51: §0.3
    card_id        uuid        NOT NULL REFERENCES cards (id) ON DELETE CASCADE,   -- #51:
                               -- scheduling state for a card that no longer exists is meaningless,
                               -- and deck delete has to be able to finish. NOTE: this makes the
                               -- card-regeneration trap in docs/schema.md live -- a note edit that
                               -- drops and re-creates cards now silently discards progress.
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

-- Backs the CASCADE on card delete (#51) -- the PK leads with user_id, so it can't serve this.
CREATE INDEX user_card_state_card_id_idx ON user_card_state (card_id);

-- +goose Down
DROP TABLE user_card_state;
