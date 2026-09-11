-- name: GetUserCardState :one
SELECT * FROM user_card_state WHERE user_id = $1 AND card_id = $2;

-- Suspend/unsuspend/bury/flag a card (#223): a settings write on the caller's own row, not a
-- scheduling write, so unlike UpsertUserCardStateOnReview this is unconditional -- there is no
-- "newer review wins" question for a column FSRS never touches. A never-seen card has no row
-- yet (migration 00010's PK is (user_id, card_id)), so this must upsert; due defaults to now()
-- on first insert, the same neutral zero-state UpsertUserCardStateOnReview's own first write
-- would produce, and is overwritten by the first real grade. sqlc.narg columns are NULL when the
-- caller isn't setting that field, and COALESCE(EXCLUDED.x, user_card_state.x) on the UPDATE arm
-- leaves an unset field untouched rather than clobbering it back to a zero value.
--
-- buried_until arrives as a timestamptz (the caller's already-shifted study_day_start + 24h, see
-- cards_state.go's studyTomorrow), cast to ::date here -- the same cast shape
-- ListDueCardsForStudy/CountQueueForDeck use to compare buried_until against study_day_start, so
-- this write's idea of "tomorrow" agrees with what those reads will later test it against
-- regardless of the session's TimeZone GUC. clear_buried disambiguates NULL's two meanings ("not
-- setting buried_until" vs. "explicitly un-burying"), since a single nullable arg can't carry
-- both -- unsuspend needs no such flag because `false` is an ordinary, non-NULL value.
-- name: UpsertUserCardStateSettings :one
INSERT INTO user_card_state (user_id, card_id, due, suspended, buried_until, flag)
VALUES (
    sqlc.arg(user_id), sqlc.arg(card_id), now(),
    COALESCE(sqlc.narg(suspended)::boolean, false),
    (sqlc.narg(buried_until)::timestamptz)::date,
    COALESCE(sqlc.narg(flag)::smallint, 0)
)
ON CONFLICT (user_id, card_id) DO UPDATE SET
    suspended    = COALESCE(sqlc.narg(suspended)::boolean, user_card_state.suspended),
    buried_until = CASE WHEN sqlc.arg(clear_buried)::boolean THEN NULL
                        ELSE COALESCE((sqlc.narg(buried_until)::timestamptz)::date, user_card_state.buried_until) END,
    flag         = COALESCE(sqlc.narg(flag)::smallint, user_card_state.flag)
RETURNING *;

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
