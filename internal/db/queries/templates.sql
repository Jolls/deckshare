-- name: ListTemplatesForNoteType :many
SELECT * FROM templates WHERE note_type_id = $1 ORDER BY ordinal;

-- name: CreateTemplate :one
INSERT INTO templates (note_type_id, ordinal, name, qfmt, afmt) VALUES ($1,$2,$3,$4,$5) RETURNING *;

-- Renames/re-formats AND repositions a kept template in one statement (#89), same reasoning as
-- UpdateFieldNameAndOrdinal -- templates.ordinal is likewise not UNIQUE-constrained.
-- name: UpdateTemplateContentAndOrdinal :execrows
UPDATE templates SET name = sqlc.arg(name), qfmt = sqlc.arg(qfmt), afmt = sqlc.arg(afmt),
                     ordinal = sqlc.arg(ordinal)
WHERE id = sqlc.arg(id) AND note_type_id = sqlc.arg(note_type_id);

-- Only safe to call once every card backed by these templates is already gone --
-- cards.template_id REFERENCES templates ON DELETE RESTRICT (docs/schema.md's Deletion policy).
-- RemapNoteTypeCards (internal/db/cards.go) always calls DeleteCardsForTemplates first.
-- name: DeleteTemplatesByIDs :execrows
DELETE FROM templates WHERE note_type_id = sqlc.arg(note_type_id) AND id = ANY(sqlc.arg(ids)::uuid[]);
