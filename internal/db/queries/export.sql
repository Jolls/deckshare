-- Export (#59, wired to GET /decks/{id}/export by #140). Every statement here is called only from
-- internal/apkg/dbexport.go, and every one is scoped two ways at once:
--
--   * by DECK -- one deck's content, not an owner's whole collection. The route is deck-scoped
--     (docs/routes.md) and a caller exporting a deck shared with them has no business reading the
--     owner's other decks. Card membership is the definition: a card is in the export iff its own
--     cards.deck_id is this deck (architecture.md §20 -- cards.deck_id is authoritative, notes.deck_id
--     is only the note's home deck), and a note is in iff it has such a card. Note types follow the
--     notes, because Anki's package format cannot carry a note without its note type.
--
--   * by CALLER -- deck_access, never owner_id (CLAUDE.md §9's "no cross-user reads without a
--     deck_access row", "authorisation is explicit at the query layer"). The deck_access join is
--     repeated on every statement rather than trusted to the handler or to GetDeckForExport alone;
--     deck_access is primary-keyed (deck_id, user_id), so the join is a guard, never a fan-out.
--
-- user_card_state/review_log are the caller's OWN rows on this deck's cards -- not the owner's. A
-- shared deck's other reviewers' progress is exactly what apkg-format.md's Export section says
-- cannot be represented in a single Anki collection.

-- name: GetDeckForExport :one
SELECT d.* FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(caller_id) AND da.can_view
WHERE d.id = sqlc.arg(deck_id);

-- name: ListNoteTypesForDeckExport :many
SELECT nt.* FROM note_types nt
JOIN deck_access da ON da.deck_id = sqlc.arg(deck_id) AND da.user_id = sqlc.arg(caller_id) AND da.can_view
WHERE EXISTS (
    SELECT 1 FROM notes n
    JOIN cards c ON c.note_id = n.id
    WHERE n.note_type_id = nt.id AND c.deck_id = sqlc.arg(deck_id)
)
ORDER BY nt.id;

-- name: ListFieldsForDeckExport :many
SELECT f.* FROM fields f
JOIN deck_access da ON da.deck_id = sqlc.arg(deck_id) AND da.user_id = sqlc.arg(caller_id) AND da.can_view
WHERE EXISTS (
    SELECT 1 FROM notes n
    JOIN cards c ON c.note_id = n.id
    WHERE n.note_type_id = f.note_type_id AND c.deck_id = sqlc.arg(deck_id)
)
ORDER BY f.note_type_id, f.ordinal;

-- name: ListTemplatesForDeckExport :many
SELECT t.* FROM templates t
JOIN deck_access da ON da.deck_id = sqlc.arg(deck_id) AND da.user_id = sqlc.arg(caller_id) AND da.can_view
WHERE EXISTS (
    SELECT 1 FROM notes n
    JOIN cards c ON c.note_id = n.id
    WHERE n.note_type_id = t.note_type_id AND c.deck_id = sqlc.arg(deck_id)
)
ORDER BY t.note_type_id, t.ordinal;

-- name: ListNotesForDeckExport :many
SELECT n.* FROM notes n
JOIN deck_access da ON da.deck_id = sqlc.arg(deck_id) AND da.user_id = sqlc.arg(caller_id) AND da.can_view
WHERE EXISTS (SELECT 1 FROM cards c WHERE c.note_id = n.id AND c.deck_id = sqlc.arg(deck_id))
ORDER BY n.id;

-- name: ListCardsForDeckExport :many
SELECT c.* FROM cards c
JOIN deck_access da ON da.deck_id = sqlc.arg(deck_id) AND da.user_id = sqlc.arg(caller_id) AND da.can_view
WHERE c.deck_id = sqlc.arg(deck_id)
ORDER BY c.id;

-- name: ListUserCardStateForDeckExport :many
SELECT ucs.* FROM user_card_state ucs
JOIN cards c ON c.id = ucs.card_id
JOIN deck_access da ON da.deck_id = sqlc.arg(deck_id) AND da.user_id = sqlc.arg(caller_id) AND da.can_view
WHERE c.deck_id = sqlc.arg(deck_id) AND ucs.user_id = sqlc.arg(caller_id);

-- name: ListReviewLogForDeckExport :many
SELECT rl.* FROM review_log rl
JOIN cards c ON c.id = rl.card_id
JOIN deck_access da ON da.deck_id = sqlc.arg(deck_id) AND da.user_id = sqlc.arg(caller_id) AND da.can_view
WHERE c.deck_id = sqlc.arg(deck_id) AND rl.user_id = sqlc.arg(caller_id)
ORDER BY rl.card_id, rl.reviewed_at;
