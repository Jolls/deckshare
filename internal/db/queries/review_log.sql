-- name: GetReviewLogEntry :one
SELECT * FROM review_log WHERE id = $1;
