-- name: GetReviewLogEntry :one
SELECT * FROM review_log WHERE id = $1;

-- The idempotency check (architecture.md §6). Deliberately NOT scoped to user_id: review_log.id is a
-- global primary key, so an id taken by anyone is taken here -- scoping it would let ON CONFLICT drop
-- the row while the state write landed.
-- name: ListExistingReviewLogIDs :many
SELECT id FROM review_log WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- Every column except id is computed server-side (CLAUDE.md §2.7). anki_id stays NULL for rows this
-- reviewer writes. execrows, not exec: 0 rows means the id was already taken and this is a pure retry,
-- which must NOT be rescheduled from the row it already advanced.
-- name: InsertReviewLog :execrows
INSERT INTO review_log (
    id, user_id, card_id, rating, reviewed_at, duration_ms,
    state_before, learning_steps_before, stability_before, difficulty_before,
    elapsed_days_before, scheduled_days_after, fsrs_version, review_kind
) VALUES (
    sqlc.arg(id), sqlc.arg(user_id), sqlc.arg(card_id), sqlc.arg(rating),
    sqlc.arg(reviewed_at), sqlc.narg(duration_ms),
    sqlc.arg(state_before), sqlc.arg(learning_steps_before),
    sqlc.arg(stability_before), sqlc.arg(difficulty_before),
    sqlc.arg(elapsed_days_before), sqlc.arg(scheduled_days_after),
    sqlc.arg(fsrs_version), sqlc.arg(review_kind)
)
ON CONFLICT (id) DO NOTHING;

-- The replay path (architecture.md §6); backed by review_log_card_id_user_id_reviewed_at_idx. Only
-- rating and reviewed_at are read: a replay re-derives every *_before itself.
-- name: ListReviewLogForCard :many
SELECT id, rating, reviewed_at
FROM review_log
WHERE user_id = sqlc.arg(user_id) AND card_id = sqlc.arg(card_id)
ORDER BY reviewed_at, id;
