-- name: GetNoteType :one
SELECT * FROM note_types WHERE id = $1;
