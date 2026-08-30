-- name: ListNotesInDeck :many
SELECT n.id, n.fields ->> nt.sort_field_idx AS sort_text, n.tags, n.modified_at, nt.name AS note_type_name,
       (SELECT count(*) FROM cards c WHERE c.note_id = n.id) AS card_count
FROM notes n
JOIN note_types nt ON nt.id = n.note_type_id
JOIN deck_access da ON da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id) AND da.can_view
WHERE n.deck_id = sqlc.arg(deck_id)
ORDER BY n.modified_at DESC
LIMIT 200;

-- Owner_id comes from the DECK, not the caller: notes.owner_id is denormalised from
-- decks.owner_id and, as of migration 00015, a composite FK rejects any other value.
-- name: CreateNote :one
INSERT INTO notes (guid, owner_id, note_type_id, deck_id, fields, tags, checksum)
SELECT sqlc.arg(guid), d.owner_id, nt.id, d.id, sqlc.arg(fields), sqlc.arg(tags), sqlc.arg(checksum)
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_edit_content
JOIN note_types nt ON nt.id = sqlc.arg(note_type_id) AND nt.owner_id = sqlc.arg(user_id)
WHERE d.id = sqlc.arg(deck_id)
RETURNING *;

-- Locks the note for the duration of the transaction and authorises the caller in one step --
-- the same no-row-means-404 contract as LockDeckForDelete. The lock is what makes the card
-- ordinal diff in SyncNoteCards atomic against a concurrent edit of the same note.
-- name: LockNoteForContentEdit :one
SELECT n.*
FROM notes n
JOIN deck_access da ON da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_edit_content
WHERE n.id = sqlc.arg(note_id)
FOR UPDATE OF n;

-- name: GetNoteForContentEdit :one
SELECT n.*
FROM notes n
JOIN deck_access da ON da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_edit_content
WHERE n.id = sqlc.arg(note_id);

-- Authorises the stronger can_manage_access permission required specifically for a note-type
-- change (#138) -- ordinary content edits use can_edit_content alone via
-- GetNoteForContentEdit/LockNoteForContentEdit. Same no-row-means-404 contract as those.
-- name: GetNoteForNoteTypeChange :one
SELECT n.*
FROM notes n
JOIN deck_access da ON da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_edit_content AND da.can_manage_access
WHERE n.id = sqlc.arg(note_id);

-- No deck_access join (CLAUDE.md §9): the caller (UpdateNoteWithCards) has already taken
-- LockNoteForContentEdit on this note, which authorises can_view + can_edit_content and holds the
-- row lock for the rest of the transaction.
-- name: UpdateNoteContent :execrows
UPDATE notes SET fields = sqlc.arg(fields), tags = sqlc.arg(tags),
                 checksum = sqlc.arg(checksum), note_type_id = sqlc.arg(note_type_id),
                 modified_at = now()
WHERE id = sqlc.arg(note_id);

-- Moving a note must move owner_id with it (docs/schema.md, "must not drift"); migration 00015
-- makes a drifted pair fail loudly instead of silently breaking the import key.
-- name: MoveNoteToDeck :execrows
UPDATE notes n
SET deck_id = d.id, owner_id = d.owner_id, modified_at = now()
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_edit_content
WHERE n.id = sqlc.arg(note_id) AND d.id = sqlc.arg(target_deck_id);

-- Cards filed in the note's OLD home deck follow it; cards deliberately filed elsewhere stay
-- put (architecture.md §20: a card belongs to exactly one deck, and a note's cards need not
-- share one).
-- name: MoveNoteCardsFromDeck :execrows
UPDATE cards SET deck_id = sqlc.arg(target_deck_id)
WHERE note_id = sqlc.arg(note_id) AND deck_id = sqlc.arg(source_deck_id);

-- name: DeleteNote :execrows
DELETE FROM notes n
USING deck_access da
WHERE n.id = sqlc.arg(note_id) AND da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id)
  AND da.can_view AND da.can_edit_content;

-- name: ListNoteIDsOfNoteType :many
SELECT id FROM notes WHERE note_type_id = $1;

-- Rewrites every note of note_type_id's fields array positionally, in one statement (#89): new
-- position i takes its value from OLD ordinal old_ordinals[i], or an empty string when
-- old_ordinals[i] is -1 (a field newly added by this same edit, which no note has ever held a
-- value for -- same '""'::jsonb sentinel AppendNoteFieldSlot used). old_ordinals encodes the full
-- old->new permutation and/or subset (a removed field simply has no entry) in one array, so this
-- costs one UPDATE regardless of note count -- it scales in rows touched, not round trips.
-- name: RemapNoteFields :execrows
UPDATE notes n
SET fields = (
    SELECT jsonb_agg(CASE WHEN t.old_ordinal < 0 THEN '""'::jsonb ELSE n.fields -> t.old_ordinal END
                     ORDER BY t.pos)
    FROM unnest(sqlc.arg(old_ordinals)::int[]) WITH ORDINALITY AS t(old_ordinal, pos)
),
    modified_at = now()
WHERE n.note_type_id = sqlc.arg(note_type_id);

-- Non-locking read used only to recompute checksum after a field remap that changes which field is
-- now first (notes.checksum is sha1-of-stripped-html of field 0 -- see db.ComputeNoteChecksum).
-- name: ListNoteFieldsForNoteType :many
SELECT id, fields FROM notes WHERE note_type_id = sqlc.arg(note_type_id);

-- name: BulkUpdateNoteChecksums :execrows
WITH v AS (
    SELECT i.note_id, c.checksum
    FROM unnest(sqlc.arg(note_ids)::uuid[]) WITH ORDINALITY AS i(note_id, ord)
    JOIN unnest(sqlc.arg(checksums)::bigint[]) WITH ORDINALITY AS c(checksum, ord)
      ON i.ord = c.ord
)
UPDATE notes n SET checksum = v.checksum FROM v WHERE n.id = v.note_id;
