-- name: GetUserCardState :one
SELECT * FROM user_card_state WHERE user_id = $1 AND card_id = $2;

-- Last-write-wins by REVIEW time, not arrival time -- the property that makes a retrying sender safe
-- (architecture.md §6). suspended / buried_until / flag are user settings, not scheduling output, and
-- are never touched here.
-- name: UpsertUserCardStateOnReview :execrows
INSERT INTO user_card_state (user_id, card_id, due, stability, difficulty, state, reps, lapses,
                             elapsed_days, scheduled_days, learning_steps, last_review)
VALUES (sqlc.arg(user_id), sqlc.arg(card_id), sqlc.arg(due), sqlc.arg(stability),
        sqlc.arg(difficulty), sqlc.arg(state), sqlc.arg(reps), sqlc.arg(lapses),
        sqlc.arg(elapsed_days), sqlc.arg(scheduled_days), sqlc.arg(learning_steps),
        sqlc.arg(last_review))
ON CONFLICT (user_id, card_id) DO UPDATE SET
    due = EXCLUDED.due, stability = EXCLUDED.stability, difficulty = EXCLUDED.difficulty,
    state = EXCLUDED.state, reps = EXCLUDED.reps, lapses = EXCLUDED.lapses,
    elapsed_days = EXCLUDED.elapsed_days, scheduled_days = EXCLUDED.scheduled_days,
    learning_steps = EXCLUDED.learning_steps, last_review = EXCLUDED.last_review
WHERE user_card_state.last_review IS NULL OR user_card_state.last_review < EXCLUDED.last_review;

-- The replay writer: unguarded, because a rebuild from review_log IS the newest truth for this card by
-- construction (architecture.md §6). Reached only through internal/review's writeReplayedState -- from
-- ReplayCard and from GradeBatch's out-of-order branch -- and only with the (user, card) advisory lock
-- held. Never call it from anywhere the lock is not already taken.
-- name: UpsertUserCardStateFromReplay :exec
INSERT INTO user_card_state (user_id, card_id, due, stability, difficulty, state, reps, lapses,
                             elapsed_days, scheduled_days, learning_steps, last_review)
VALUES (sqlc.arg(user_id), sqlc.arg(card_id), sqlc.arg(due), sqlc.arg(stability),
        sqlc.arg(difficulty), sqlc.arg(state), sqlc.arg(reps), sqlc.arg(lapses),
        sqlc.arg(elapsed_days), sqlc.arg(scheduled_days), sqlc.arg(learning_steps),
        sqlc.arg(last_review))
ON CONFLICT (user_id, card_id) DO UPDATE SET
    due = EXCLUDED.due, stability = EXCLUDED.stability, difficulty = EXCLUDED.difficulty,
    state = EXCLUDED.state, reps = EXCLUDED.reps, lapses = EXCLUDED.lapses,
    elapsed_days = EXCLUDED.elapsed_days, scheduled_days = EXCLUDED.scheduled_days,
    learning_steps = EXCLUDED.learning_steps, last_review = EXCLUDED.last_review;
