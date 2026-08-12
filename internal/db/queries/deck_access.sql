-- name: GetDeckAccess :one
SELECT * FROM deck_access WHERE deck_id = $1 AND user_id = $2;
