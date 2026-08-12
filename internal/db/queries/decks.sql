-- name: GetDeck :one
SELECT * FROM decks WHERE id = $1;
