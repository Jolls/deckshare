-- name: GetField :one
SELECT * FROM fields WHERE id = $1;
