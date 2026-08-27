-- name: GetSession :one
SELECT * FROM sessions WHERE id = $1;

-- name: CreateSession :exec
INSERT INTO sessions (id, user_id, expires_at) VALUES ($1, $2, $3);

-- name: GetSessionUser :one
SELECT sqlc.embed(u), s.expires_at
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.id = $1 AND s.expires_at > now();

-- name: RenewSession :exec
UPDATE sessions SET expires_at = $2 WHERE id = $1;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = $1;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at < now();

-- name: DeleteSessionsForUser :execrows
DELETE FROM sessions WHERE user_id = $1;
