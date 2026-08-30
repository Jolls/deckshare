-- name: ListFieldsForNoteType :many
SELECT * FROM fields WHERE note_type_id = $1 ORDER BY ordinal;

-- name: ListFieldsForNoteTypes :many
SELECT note_type_id, ordinal, name FROM fields
WHERE note_type_id = ANY(sqlc.arg(note_type_ids)::uuid[])
ORDER BY note_type_id, ordinal;

-- name: CreateField :one
INSERT INTO fields (note_type_id, ordinal, name) VALUES ($1, $2, $3) RETURNING *;

-- Renames AND repositions a kept field in one statement (#89): fields.ordinal has no UNIQUE
-- constraint (migrations/00004_fields.sql -- deliberately, for exactly this reason), so an
-- in-place ordinal reassignment, including a full permutation, is always collision-free.
-- name: UpdateFieldNameAndOrdinal :execrows
UPDATE fields SET name = sqlc.arg(name), ordinal = sqlc.arg(ordinal)
WHERE id = sqlc.arg(id) AND note_type_id = sqlc.arg(note_type_id);

-- name: DeleteField :execrows
DELETE FROM fields WHERE id = sqlc.arg(id) AND note_type_id = sqlc.arg(note_type_id);
