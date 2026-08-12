-- name: GetCard :one
SELECT * FROM cards WHERE id = $1;
