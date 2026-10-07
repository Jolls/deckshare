-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE lower(email) = lower(sqlc.arg(email));

-- name: EmailExists :one
SELECT EXISTS (SELECT 1 FROM users WHERE lower(email) = lower(sqlc.arg(email)));

-- name: CreateUser :one
INSERT INTO users (email, password_hash, display_name, last_seen_version)
VALUES ($1, $2, $3, $4)
ON CONFLICT (lower(email)) DO NOTHING
RETURNING *;

-- name: UpdateUserProfile :exec
UPDATE users SET display_name = $2, timezone = $3, day_start_hour = $4 WHERE id = $1;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2 WHERE id = $1;

-- Avatar changes are a distinct operation from a profile edit (#176): different input, and it must
-- be independently clearable -- passing NULL is how an avatar is removed, so there is no separate
-- clear query. The sha256 must already exist in media_blobs; the FK is what enforces that.
-- name: UpdateUserAvatar :exec
UPDATE users SET avatar_sha256 = $2 WHERE id = $1;

-- Appearance (#268). Keyed only on the session user's id -- no id ever comes from the form.
-- name: UpdateUserColorScheme :exec
UPDATE users SET color_scheme = $2 WHERE id = $1;

-- Release notes (#266). Keyed only on the session user's id -- the version comes from the running
-- binary, never the form.
-- name: UpdateUserLastSeenVersion :exec
UPDATE users SET last_seen_version = $2 WHERE id = $1;
