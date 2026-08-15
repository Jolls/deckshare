-- name: GetField :one
SELECT * FROM fields WHERE id = $1;

-- name: ListFieldsForNoteType :many
SELECT * FROM fields WHERE note_type_id = $1 ORDER BY ordinal;

-- name: ListFieldsForNoteTypes :many
SELECT note_type_id, ordinal, name FROM fields
WHERE note_type_id = ANY(sqlc.arg(note_type_ids)::uuid[])
ORDER BY note_type_id, ordinal;

-- name: CreateField :one
INSERT INTO fields (note_type_id, ordinal, name) VALUES ($1, $2, $3) RETURNING *;

-- name: RenameField :execrows
UPDATE fields SET name = sqlc.arg(name) WHERE id = sqlc.arg(id) AND note_type_id = sqlc.arg(note_type_id);

-- name: DeleteFieldsForNoteType :execrows
DELETE FROM fields WHERE note_type_id = $1;
