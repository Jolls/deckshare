-- name: ListTemplatesForNoteType :many
SELECT * FROM templates WHERE note_type_id = $1 ORDER BY ordinal;

-- name: CreateTemplate :one
INSERT INTO templates (note_type_id, ordinal, name, qfmt, afmt) VALUES ($1,$2,$3,$4,$5) RETURNING *;

-- name: UpdateTemplate :execrows
UPDATE templates SET name = sqlc.arg(name), qfmt = sqlc.arg(qfmt), afmt = sqlc.arg(afmt)
WHERE id = sqlc.arg(id) AND note_type_id = sqlc.arg(note_type_id);

-- name: DeleteTemplatesForNoteType :execrows
DELETE FROM templates WHERE note_type_id = $1;
