-- Export (#59). Every statement here is called only from internal/apkg/dbexport.go, reading one
-- owner's own content -- scoped directly by owner_id, the same convention import.sql uses,
-- not deck_access: exporting is always the owner's own collection, never someone else's shared
-- deck (architecture.md §7's Export section).

-- name: ListDecksByOwner :many
SELECT * FROM decks WHERE owner_id = sqlc.arg(owner_id) ORDER BY id;

-- name: ListNoteTypesByOwner :many
SELECT * FROM note_types WHERE owner_id = sqlc.arg(owner_id) ORDER BY id;

-- name: ListFieldsForOwner :many
SELECT f.* FROM fields f
JOIN note_types nt ON nt.id = f.note_type_id
WHERE nt.owner_id = sqlc.arg(owner_id)
ORDER BY f.note_type_id, f.ordinal;

-- name: ListTemplatesForOwner :many
SELECT t.* FROM templates t
JOIN note_types nt ON nt.id = t.note_type_id
WHERE nt.owner_id = sqlc.arg(owner_id)
ORDER BY t.note_type_id, t.ordinal;

-- name: ListNotesByOwner :many
SELECT * FROM notes WHERE owner_id = sqlc.arg(owner_id) ORDER BY id;

-- name: ListCardsByOwner :many
SELECT c.* FROM cards c
JOIN notes n ON n.id = c.note_id
WHERE n.owner_id = sqlc.arg(owner_id)
ORDER BY c.id;

-- Only this user's OWN progress on their OWN cards -- a shared deck's other reviewers' state is
-- exactly what apkg-format.md's Export section says cannot be represented in a single collection.
-- name: ListUserCardStateForOwnerExport :many
SELECT ucs.* FROM user_card_state ucs
JOIN cards c ON c.id = ucs.card_id
JOIN notes n ON n.id = c.note_id
WHERE n.owner_id = sqlc.arg(owner_id) AND ucs.user_id = sqlc.arg(owner_id);

-- name: ListReviewLogForOwnerExport :many
SELECT rl.* FROM review_log rl
JOIN cards c ON c.id = rl.card_id
JOIN notes n ON n.id = c.note_id
WHERE n.owner_id = sqlc.arg(owner_id) AND rl.user_id = sqlc.arg(owner_id)
ORDER BY rl.card_id, rl.reviewed_at;
