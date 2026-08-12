-- name: GetUserCardState :one
SELECT * FROM user_card_state WHERE user_id = $1 AND card_id = $2;
