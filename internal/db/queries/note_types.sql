-- name: GetNoteType :one
SELECT * FROM note_types WHERE id = $1;

-- Authority for a note type derives from the decks whose notes use it, never from
-- note_types.owner_id -- see docs/plans/192-note-type-authority.md. owner_id is a namespace key
-- only (UNIQUE (owner_id, name), re-import idempotence, CLAUDE.md §2.2).
--
-- READABLE(nt, user): owns it, or holds can_view on any deck holding a note that uses it.
-- WRITABLE(nt, user): no deck holding a note that uses it lacks can_view+can_edit_content for
-- user, and (some note uses it, or user owns it) -- the trailing owner clause is load-bearing:
-- NOT EXISTS over an empty deck set is vacuously true, so without it an unused note type would
-- be writable by everyone.

-- name: ListNoteTypesForUser :many
SELECT nt.*,
  (SELECT count(*) FROM notes n WHERE n.note_type_id = nt.id) AS note_count,
  (SELECT count(DISTINCT n.deck_id) FROM notes n WHERE n.note_type_id = nt.id) AS deck_count,
  (
    NOT EXISTS (
      SELECT 1 FROM (SELECT DISTINCT n.deck_id FROM notes n WHERE n.note_type_id = nt.id) d
      WHERE NOT EXISTS (
        SELECT 1 FROM deck_access da
        WHERE da.deck_id = d.deck_id AND da.user_id = sqlc.arg(user_id)
          AND da.can_view AND da.can_edit_content))
    AND (EXISTS (SELECT 1 FROM notes n WHERE n.note_type_id = nt.id) OR nt.owner_id = sqlc.arg(user_id))
  ) AS can_edit
FROM note_types nt
WHERE nt.owner_id = sqlc.arg(user_id)
   OR EXISTS (
     SELECT 1 FROM notes n
     JOIN deck_access da ON da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id) AND da.can_view
     WHERE n.note_type_id = nt.id)
ORDER BY nt.name;

-- name: GetNoteTypeForRead :one
SELECT nt.* FROM note_types nt
WHERE nt.id = sqlc.arg(id)
  AND (
    nt.owner_id = sqlc.arg(user_id)
    OR EXISTS (
      SELECT 1 FROM notes n
      JOIN deck_access da ON da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id) AND da.can_view
      WHERE n.note_type_id = nt.id)
  );

-- Non-locking WRITABLE check for display purposes only -- e.g. GET /note-types/{id}/edit
-- deciding between the edit form and the read-only view. The real edit transaction still
-- re-acquires LockNoteTypeForEdit's row lock properly; this must never gate a write.
-- name: CanEditNoteType :one
SELECT
  NOT EXISTS (
    SELECT 1 FROM (SELECT DISTINCT n.deck_id FROM notes n WHERE n.note_type_id = nt.id) d
    WHERE NOT EXISTS (
      SELECT 1 FROM deck_access da
      WHERE da.deck_id = d.deck_id AND da.user_id = sqlc.arg(user_id)
        AND da.can_view AND da.can_edit_content))
  AND (EXISTS (SELECT 1 FROM notes n WHERE n.note_type_id = nt.id) OR nt.owner_id = sqlc.arg(user_id))
  AS can_edit
FROM note_types nt WHERE nt.id = sqlc.arg(id);

-- Locks the note type row for the duration of an edit transaction, serialising it against a
-- concurrent edit of the same note type (docs/plans/54's TOCTOU note: the noteCount read below
-- and the structural field/template writes must not straddle a concurrent change).
-- name: LockNoteTypeForEdit :one
SELECT nt.* FROM note_types nt
WHERE nt.id = sqlc.arg(id)
  AND NOT EXISTS (
    SELECT 1 FROM (SELECT DISTINCT n.deck_id FROM notes n WHERE n.note_type_id = nt.id) d
    WHERE NOT EXISTS (
      SELECT 1 FROM deck_access da
      WHERE da.deck_id = d.deck_id AND da.user_id = sqlc.arg(user_id)
        AND da.can_view AND da.can_edit_content))
  AND (EXISTS (SELECT 1 FROM notes n WHERE n.note_type_id = nt.id) OR nt.owner_id = sqlc.arg(user_id))
FOR UPDATE;

-- name: CreateNoteType :one
INSERT INTO note_types (owner_id, name, css, is_cloze, sort_field_idx)
VALUES ($1, $2, $3, $4, $5) RETURNING *;

-- is_cloze is immutable after creation: flipping it changes what every existing note's cards
-- mean. Not editable in the form, not settable here.
-- name: UpdateNoteTypeRow :execrows
UPDATE note_types nt SET name = sqlc.arg(name), css = sqlc.arg(css),
                      sort_field_idx = sqlc.arg(sort_field_idx)
WHERE nt.id = sqlc.arg(id)
  AND NOT EXISTS (
    SELECT 1 FROM (SELECT DISTINCT n.deck_id FROM notes n WHERE n.note_type_id = nt.id) d
    WHERE NOT EXISTS (
      SELECT 1 FROM deck_access da
      WHERE da.deck_id = d.deck_id AND da.user_id = sqlc.arg(user_id)
        AND da.can_view AND da.can_edit_content))
  AND (EXISTS (SELECT 1 FROM notes n WHERE n.note_type_id = nt.id) OR nt.owner_id = sqlc.arg(user_id));

-- name: CountNotesOfNoteType :one
SELECT count(*) FROM notes WHERE note_type_id = $1;

-- notes.note_type_id ON DELETE RESTRICT blocks this while any note exists (routes.md);
-- fields and templates cascade. The handler turns 23503 into 409. Delete stays owner-scoped: a
-- note type with no notes has no decks, so WRITABLE would fall through to the owner anyway
-- (docs/plans/192-note-type-authority.md), and staying owner-scoped keeps the existing 23503 ->
-- 409 error shape rather than a 404.
-- name: DeleteNoteType :execrows
DELETE FROM note_types WHERE id = sqlc.arg(id) AND owner_id = sqlc.arg(owner_id);

-- Non-locking preview read for the #89 structural-change confirmation page. Same tolerance as
-- #138's ListCardsForNote: a stale preview here is never a correctness problem, since the actual
-- mutation re-reads everything fresh under LockNoteTypeForEdit's row lock. deck_count isn't
-- selected here -- the confirmation page gets the affected decks' names from
-- ListDecksUsingNoteType instead (docs/plans/192-note-type-authority.md).
-- name: NoteTypeOtherUserCount :one
SELECT count(DISTINCT da.user_id) FROM notes n
JOIN deck_access da ON da.deck_id = n.deck_id
WHERE n.note_type_id = sqlc.arg(note_type_id) AND da.user_id != sqlc.arg(owner_id);

-- The decks whose notes use note_type_id, for the structural-change confirmation page and the
-- notetypes list's denial messages. visible = the caller holds can_view on that deck (whether or
-- not the name may be rendered -- the caller must not render name when visible is false).
-- editable = the caller holds can_view+can_edit_content, i.e. this deck alone does not block
-- WRITABLE; a row with editable false is one of the (possibly several) decks actually blocking
-- the caller's edit. On the confirmation page every row has both true -- reaching it requires
-- WRITABLE, which implies can_view+can_edit_content on every deck using the note type.
-- name: ListDecksUsingNoteType :many
SELECT d.id AS deck_id, d.name,
  EXISTS (
    SELECT 1 FROM deck_access da WHERE da.deck_id = d.id AND da.user_id = sqlc.arg(user_id) AND da.can_view
  ) AS visible,
  EXISTS (
    SELECT 1 FROM deck_access da
    WHERE da.deck_id = d.id AND da.user_id = sqlc.arg(user_id) AND da.can_view AND da.can_edit_content
  ) AS editable
FROM decks d
WHERE d.id IN (SELECT DISTINCT n.deck_id FROM notes n WHERE n.note_type_id = sqlc.arg(note_type_id))
ORDER BY d.name;

-- The READABLE note types for deck_id's new-note-form picker, flagging which ones a note in
-- deck_id already uses -- lets a collaborator with only can_edit_content on someone else's deck
-- select that deck's existing note types even though they own none of them.
-- name: ListNoteTypesForNoteForm :many
SELECT nt.*,
  (SELECT count(*) FROM notes n WHERE n.note_type_id = nt.id AND n.deck_id = sqlc.arg(deck_id)) > 0 AS in_this_deck
FROM note_types nt
WHERE nt.owner_id = sqlc.arg(user_id)
   OR EXISTS (
     SELECT 1 FROM notes n
     JOIN deck_access da ON da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id) AND da.can_view
     WHERE n.note_type_id = nt.id)
ORDER BY in_this_deck DESC, nt.name;

-- Note types readable by user_id that a note currently on current_note_type_id could switch to
-- without cross-field-set remapping (#138 v1): same is_cloze flag, and the same field names in
-- the same ordinal order. Deliberately includes current_note_type_id itself (harmless no-op
-- selection) so the caller/template doesn't need a special case to pre-select the current value.
-- name: ListFieldCompatibleNoteTypesForUser :many
SELECT nt.* FROM note_types nt
WHERE (
    nt.owner_id = sqlc.arg(user_id)
    OR EXISTS (
      SELECT 1 FROM notes n
      JOIN deck_access da ON da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id) AND da.can_view
      WHERE n.note_type_id = nt.id)
  )
  AND nt.is_cloze = sqlc.arg(is_cloze)
  AND (SELECT array_agg(f.name ORDER BY f.ordinal) FROM fields f WHERE f.note_type_id = nt.id)
      = (SELECT array_agg(f2.name ORDER BY f2.ordinal) FROM fields f2
         WHERE f2.note_type_id = sqlc.arg(current_note_type_id))
ORDER BY nt.name;
