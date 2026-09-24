-- name: CreatePasswordResetToken :exec
INSERT INTO password_reset_tokens (id, user_id, expires_at) VALUES ($1, $2, $3);

-- Peek without consuming: backs GET /reset-password, which renders the form but must not spend
-- the token (a prefetching browser or a link preview would otherwise burn it).
-- name: GetPasswordResetToken :one
SELECT user_id FROM password_reset_tokens WHERE id = $1 AND expires_at > now();

-- Single-use, atomically: the DELETE is the authorisation. Two concurrent submissions of the same
-- link race on one row and exactly one gets RETURNING output; the other sees pgx.ErrNoRows.
-- name: ConsumePasswordResetToken :one
DELETE FROM password_reset_tokens WHERE id = $1 AND expires_at > now() RETURNING user_id;

-- name: DeletePasswordResetTokensForUser :execrows
DELETE FROM password_reset_tokens WHERE user_id = $1;

-- name: DeleteExpiredPasswordResetTokens :execrows
DELETE FROM password_reset_tokens WHERE expires_at < now();
