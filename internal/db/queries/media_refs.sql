-- name: GetMediaRef :one
SELECT * FROM media_refs WHERE deck_id = $1 AND filename = $2;
