-- name: GetTemplate :one
SELECT * FROM templates WHERE id = $1;
