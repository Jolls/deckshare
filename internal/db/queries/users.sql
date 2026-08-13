-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE lower(email) = lower(sqlc.arg(email));

-- name: EmailExists :one
SELECT EXISTS (SELECT 1 FROM users WHERE lower(email) = lower(sqlc.arg(email)));

-- name: CreateUser :one
INSERT INTO users (email, password_hash, display_name)
VALUES ($1, $2, $3)
ON CONFLICT (lower(email)) DO NOTHING
RETURNING *;

-- name: UpdateUserProfile :exec
UPDATE users SET display_name = $2, timezone = $3, day_start_hour = $4 WHERE id = $1;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2 WHERE id = $1;
