-- name: GetNote :one
SELECT * FROM notes WHERE id = $1;
