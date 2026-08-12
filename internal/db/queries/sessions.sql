-- name: GetSession :one
SELECT * FROM sessions WHERE id = $1;
