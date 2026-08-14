-- name: GetNoteType :one
SELECT * FROM note_types WHERE id = $1;

-- name: ListNoteTypesForOwner :many
SELECT nt.*, (SELECT count(*) FROM notes n WHERE n.note_type_id = nt.id) AS note_count
FROM note_types nt WHERE nt.owner_id = sqlc.arg(owner_id) ORDER BY nt.name;

-- name: GetNoteTypeForOwner :one
SELECT * FROM note_types WHERE id = sqlc.arg(id) AND owner_id = sqlc.arg(owner_id);

-- Locks the note type row for the duration of an edit transaction, serialising it against a
-- concurrent edit of the same note type (docs/plans/54's TOCTOU note: the noteCount read below
-- and the structural field/template writes must not straddle a concurrent change).
-- name: LockNoteTypeForOwner :one
SELECT * FROM note_types WHERE id = sqlc.arg(id) AND owner_id = sqlc.arg(owner_id) FOR UPDATE;

-- name: CreateNoteType :one
INSERT INTO note_types (owner_id, name, css, is_cloze, sort_field_idx)
VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- is_cloze is immutable after creation: flipping it changes what every existing note's cards
-- mean. Not editable in the form, not settable here.
-- name: UpdateNoteTypeRow :execrows
UPDATE note_types SET name = sqlc.arg(name), css = sqlc.arg(css),
                      sort_field_idx = sqlc.arg(sort_field_idx)
WHERE id = sqlc.arg(id) AND owner_id = sqlc.arg(owner_id);

-- name: CountNotesOfNoteType :one
SELECT count(*) FROM notes WHERE note_type_id = $1;

-- notes.note_type_id ON DELETE RESTRICT blocks this while any note exists (routes.md);
-- fields and templates cascade. The handler turns 23503 into 409.
-- name: DeleteNoteType :execrows
DELETE FROM note_types WHERE id = sqlc.arg(id) AND owner_id = sqlc.arg(owner_id);
