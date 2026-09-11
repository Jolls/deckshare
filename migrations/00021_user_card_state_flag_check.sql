-- +goose Up
-- #223: flag is a plain Anki-style colour code (0 = none, 1-7 = the seven flag colours), not the
-- comment-flag of #207/card_flags -- that's a different feature on a different table. No prior
-- CHECK exists (migration 00010), so out-of-range values have been silently accepted; add the
-- constraint with the first code path that ever writes this column.
ALTER TABLE user_card_state
    ADD CONSTRAINT user_card_state_flag_check CHECK (flag BETWEEN 0 AND 7);

-- +goose Down
ALTER TABLE user_card_state DROP CONSTRAINT user_card_state_flag_check;
