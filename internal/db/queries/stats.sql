-- The learner stats page (#261, docs/plans/261-learner-stats-page.md). Every query reads only the
-- caller's own user_card_state / review_log rows, restricted to decks the caller holds can_study
-- on -- no other user's data is reachable from here, so unlike progress.sql there is no
-- can_view_progress exception involved.

-- One row per (seen card) across the caller's can_study decks, for the Recall fold in Go. Same
-- columns as ListStudentCardStateForDeck (progress.sql), which Recall now must agree with.
-- name: ListStatsCardStateForUser :many
SELECT c.deck_id, ucs.stability, ucs.difficulty, ucs.state, ucs.last_review
FROM user_card_state ucs
JOIN cards c ON c.id = ucs.card_id
JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
WHERE ucs.user_id = sqlc.arg(user_id)
ORDER BY c.deck_id;

-- Pass rate 30d / Reviews 30d per deck: review-state answers only, the same predicates as the
-- reviewstats CTE in ListStudentProgressForDeck (progress.sql).
-- name: ListStatsReviewCountsForUser :many
SELECT c.deck_id,
       (count(*) FILTER (WHERE rl.rating > 1))::bigint AS pass_count,
       count(*)::bigint                                AS review_count
FROM review_log rl
JOIN cards c ON c.id = rl.card_id
JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
WHERE rl.user_id = sqlc.arg(user_id)
  AND rl.state_before = 2
  AND rl.reviewed_at >= sqlc.arg(now)::timestamptz - make_interval(days => sqlc.arg(window_days)::int)
GROUP BY c.deck_id;

-- Overall per-day counts for the two charts, bucketed by the caller's own study day (users.timezone
-- and day_start_hour -- the same shift GetStudyDayWindow applies), from first_day onward. Days
-- with no reviews have no row; the handler zero-fills them.
-- name: ListStatsDailyReviewsForUser :many
SELECT ((rl.reviewed_at AT TIME ZONE u.timezone) - make_interval(hours => u.day_start_hour::int))::date AS day,
       count(*)::bigint                                AS review_count,
       (count(*) FILTER (WHERE rl.rating > 1))::bigint AS pass_count
FROM review_log rl
JOIN users u ON u.id = rl.user_id
JOIN cards c ON c.id = rl.card_id
JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
WHERE rl.user_id = sqlc.arg(user_id)
  AND rl.state_before = 2
  AND ((rl.reviewed_at AT TIME ZONE u.timezone) - make_interval(hours => u.day_start_hour::int))::date >= sqlc.arg(first_day)::date
GROUP BY 1
ORDER BY 1;
