-- Teaching-order keyset pagination (#90, needed by #238's release-day pacing: assigning lesson
-- numbers across a 500-card deck needs more than the old flat 200-row cap). Sort key is the
-- note's earliest card's import queue position -- (import_due_position, id) is the order new
-- cards are actually introduced in (reviews.sql), which is what "the first 40 cards" means to a
-- teacher, not recently-edited-first. A cloze note can hold several cards/positions; MIN takes
-- the earliest. NULL (manually created cards, or a note with no cards yet) sorts last via the
-- same 2147483647 sentinel reviews.sql's COALESCE uses for import_due_position, cast to bigint
-- for headroom against negative Anki due positions. at_start (not a cursor_sort_key/cursor_id
-- sentinel value) marks the first page, mirroring review.Cursor's own AtStart field
-- (internal/review/types.go) -- cursor_sort_key/cursor_id are meaningless when at_start is true.
-- Keyset, not offset (#238 comment): bulk-editing this list mutates modified_at, which would
-- reshuffle an offset page mid-edit; import_due_position never changes after import.
-- name: ListNotesInDeck :many
WITH ordered_notes AS (
    SELECT n.id, n.fields ->> nt.sort_field_idx AS sort_text, n.tags, n.modified_at, nt.name AS note_type_name,
           n.release_day,   -- the lesson this note is assigned to (#242); gate-only, never an order
           count(c.id) AS card_count,
           COALESCE(min(c.import_due_position), 2147483647)::bigint AS sort_key,
           -- Whether every one of this note's cards is currently suspended for the CALLER (#223):
           -- feeds the per-note Suspend/Unsuspend toggle's label (deck.html). bool_and over a
           -- LEFT JOIN so a never-seen card (no user_card_state row) counts as "not suspended"
           -- rather than dropping out of the aggregate. card_count/sort_key reuse this same
           -- cards join instead of their own correlated subqueries -- one join-and-aggregate over
           -- cards/user_card_state rather than three separate touches of the same table (#223 review).
           bool_and(COALESCE(ucs.suspended, false)) AS all_suspended
    FROM notes n
    JOIN note_types nt ON nt.id = n.note_type_id
    JOIN deck_access da ON da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id) AND da.can_view
    LEFT JOIN cards c ON c.note_id = n.id
    LEFT JOIN user_card_state ucs ON ucs.user_id = sqlc.arg(user_id) AND ucs.card_id = c.id
    WHERE n.deck_id = sqlc.arg(deck_id)
    GROUP BY n.id, nt.sort_field_idx, nt.name
)
SELECT id, sort_text, tags, modified_at, note_type_name, release_day, card_count, sort_key, all_suspended
FROM ordered_notes
WHERE sqlc.arg(at_start)::boolean
   OR (sort_key, id) > (sqlc.arg(cursor_sort_key)::bigint, sqlc.arg(cursor_id)::uuid)
ORDER BY sort_key ASC, id ASC
LIMIT sqlc.arg(limit_count);

-- Owner_id comes from the DECK, not the caller: notes.owner_id is denormalised from
-- decks.owner_id and, as of migration 00015, a composite FK rejects any other value.
-- The note type only needs to be READABLE (docs/plans/192-note-type-authority.md), not owned:
-- a collaborator with can_edit_content on this deck may author a note using any note type they
-- can read, including one already in use elsewhere on this deck that they don't own
-- (ListNoteTypesForNoteForm's "Used in this deck" picker offers exactly those).
-- name: CreateNote :one
INSERT INTO notes (guid, owner_id, note_type_id, deck_id, fields, tags, checksum)
SELECT sqlc.arg(guid), d.owner_id, nt.id, d.id, sqlc.arg(fields), sqlc.arg(tags), sqlc.arg(checksum)
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_edit_content
JOIN note_types nt ON nt.id = sqlc.arg(note_type_id)
                   AND (nt.owner_id = sqlc.arg(user_id)
                        OR EXISTS (
                          SELECT 1 FROM notes rn
                          JOIN deck_access rda ON rda.deck_id = rn.deck_id AND rda.user_id = sqlc.arg(user_id) AND rda.can_view
                          WHERE rn.note_type_id = nt.id))
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
SELECT n.*, da.can_manage_access
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

-- Bulk selection surface on the deck notes list (#241). Same per-row authorization shape as
-- DeleteNote above -- da.deck_id = n.deck_id, not trusted from the caller -- so a note id
-- smuggled into the selection is authorized against its OWN deck, never the route's. Also
-- scoped to n.deck_id = sqlc.arg(deck_id) so a bulk action launched from one deck's notes list
-- only ever touches notes actually listed there, even a smuggled id from a different deck the
-- caller can legitimately edit.
-- name: BulkDeleteNotes :execrows
DELETE FROM notes n
USING deck_access da
WHERE n.id = ANY(sqlc.arg(note_ids)::uuid[]) AND n.deck_id = sqlc.arg(deck_id)
  AND da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id)
  AND da.can_view AND da.can_edit_content;

-- Adds tags idempotently and preserves every tag already present: array_agg(DISTINCT ...) over
-- the union collapses a tag that was already there with the one being added, so replaying the
-- same bulk add is a no-op the second time.
-- name: BulkAddNoteTags :execrows
UPDATE notes n
SET tags = (SELECT array_agg(DISTINCT t ORDER BY t) FROM unnest(n.tags || sqlc.arg(tags)::text[]) AS t),
    modified_at = now()
FROM deck_access da
WHERE n.id = ANY(sqlc.arg(note_ids)::uuid[]) AND n.deck_id = sqlc.arg(deck_id)
  AND da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id)
  AND da.can_view AND da.can_edit_content;

-- Assigns the lesson a selection of notes belongs to (#242) -- the same bulk surface and the same
-- per-row authorization shape as the tag actions below. 0 is how a lesson is un-assigned: it is
-- the column's own default and means "available immediately", so there is no separate clear path.
-- can_edit_content, not can_edit_settings: the lesson map is content, the class calendar is
-- settings. Accepted consequence (#242): a collaborator holding can_edit_content can re-assign
-- release days and so unlock ahead. Students hold can_view + can_study only, so this reaches
-- co-authors, not the class.
-- name: BulkSetNoteReleaseDay :execrows
UPDATE notes n
SET release_day = sqlc.arg(release_day)::int,
    modified_at = now()
FROM deck_access da
WHERE n.id = ANY(sqlc.arg(note_ids)::uuid[]) AND n.deck_id = sqlc.arg(deck_id)
  AND da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id)
  AND da.can_view AND da.can_edit_content;

-- The deck page's calendar view (#242): how many notes are assigned to each class day. Day 0 is
-- the "no lesson assigned, available immediately" bucket.
-- name: CountNotesByReleaseDay :many
SELECT n.release_day, count(*)::bigint AS note_count
FROM notes n
JOIN deck_access da ON da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id) AND da.can_view
WHERE n.deck_id = sqlc.arg(deck_id)
GROUP BY 1
ORDER BY 1;

-- #223: suspend (or unsuspend, toggling) every card under one note, for the caller's own
-- user_card_state rows only. Authorised on can_study (not can_edit_content, unlike the other
-- bulk-* routes in this file, which edit deck content) -- this writes the caller's own
-- scheduling state. A never-seen card has no row yet, so this upserts one per card with the
-- same due=now() neutral default as UpsertUserCardStateSettings.
-- name: ToggleSuspendCardsForNote :execrows
WITH target_cards AS (
    SELECT c.id AS card_id
    FROM cards c
    JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                       AND da.can_view AND da.can_study
    WHERE c.note_id = sqlc.arg(note_id) AND c.deck_id = sqlc.arg(deck_id)
), currently AS (
    SELECT bool_and(COALESCE(ucs.suspended, false)) AS all_suspended
    FROM target_cards tc
    LEFT JOIN user_card_state ucs ON ucs.user_id = sqlc.arg(user_id) AND ucs.card_id = tc.card_id
)
INSERT INTO user_card_state (user_id, card_id, due, suspended)
SELECT sqlc.arg(user_id), tc.card_id, now(), NOT currently.all_suspended
FROM target_cards tc, currently
ON CONFLICT (user_id, card_id) DO UPDATE
SET suspended = EXCLUDED.suspended;

-- Removes tags idempotently and leaves every unrelated tag untouched. COALESCE keeps a note
-- that loses its last tag at '{}' rather than NULL (notes.tags is NOT NULL, migration 00008).
-- name: BulkRemoveNoteTags :execrows
UPDATE notes n
SET tags = COALESCE(
        (SELECT array_agg(t) FROM unnest(n.tags) AS t WHERE t <> ALL(sqlc.arg(tags)::text[])),
        '{}'
    ),
    modified_at = now()
FROM deck_access da
WHERE n.id = ANY(sqlc.arg(note_ids)::uuid[]) AND n.deck_id = sqlc.arg(deck_id)
  AND da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id)
  AND da.can_view AND da.can_edit_content;
