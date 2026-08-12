-- name: GetUserFsrsParams :one
SELECT * FROM user_fsrs_params WHERE id = $1;
