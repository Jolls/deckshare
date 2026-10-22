-- Cards are content addressing only -- no scheduling columns exist here to lose (CLAUDE.md §2.1).
-- These four statements are the whole of card regeneration; see internal/db/cards.go for the
-- diff that calls them and docs/schema.md's card-regeneration trap for why it is a diff.

-- name: ListCardsForNoteForUpdate :many
SELECT id, ordinal, template_id, deck_id FROM cards WHERE note_id = $1 ORDER BY ordinal FOR UPDATE;

-- Called once per card in the create set (§0.3's "create" batch is small -- at most one row
-- per template/cloze ordinal) rather than as a single multi-row statement: sqlc's query
-- analyzer cannot resolve a two-array unnest(...) without a live database catalog.
--
-- A card created in DeckShare joins the end of its deck's new-card queue (#240): the deck's
-- highest import_due_position + 1, or 1 for a deck with none. Without this it stayed NULL and
-- sorted after every imported card, including ones imported later. Not serialised: two concurrent
-- creates in one deck can share a position, and every reader breaks ties on card id (uuidv7, so
-- creation order). LEAST caps at the integer column's maximum; a capped card ties with
-- position-less cards at deckshare_new_card_key's sentinel, which the id tie-break handles.
-- name: CreateCard :one
INSERT INTO cards (note_id, template_id, ordinal, deck_id, import_due_position)
SELECT sqlc.arg(note_id), sqlc.arg(template_id), sqlc.arg(ordinal), sqlc.arg(deck_id),
       LEAST(COALESCE(max(c.import_due_position), 0)::bigint + 1, 2147483647)::integer
FROM cards c
WHERE c.deck_id = sqlc.arg(deck_id)
RETURNING *;

-- name: DeleteCardsByOrdinals :execrows
DELETE FROM cards WHERE note_id = sqlc.arg(note_id) AND ordinal = ANY(sqlc.arg(ordinals)::int[]);

-- One card per existing note when a template is appended to a non-cloze note type (#54 §0.5).
-- Filed in each note's home deck: notes.deck_id is the default for cards generated later
-- (architecture.md §20). Each deck's new cards go after its highest import_due_position, numbered
-- in note-id (creation) order -- CreateCard's rule (#240), applied per deck. A row skipped by
-- ON CONFLICT still uses up its number; gaps are harmless because only the order matters.
-- name: CreateCardsForNewTemplate :execrows
WITH targets AS (
    SELECT n.id AS note_id, n.deck_id
    FROM notes n
    WHERE n.note_type_id = sqlc.arg(note_type_id)
), deck_max AS (
    SELECT t.deck_id, COALESCE(max(c.import_due_position), 0)::bigint AS max_pos
    FROM (SELECT DISTINCT deck_id FROM targets) t
    LEFT JOIN cards c ON c.deck_id = t.deck_id
    GROUP BY t.deck_id
)
INSERT INTO cards (note_id, template_id, ordinal, deck_id, import_due_position)
SELECT t.note_id, sqlc.arg(template_id), sqlc.arg(ordinal), t.deck_id,
       LEAST(dm.max_pos + row_number() OVER (PARTITION BY t.deck_id ORDER BY t.note_id), 2147483647)::integer
FROM targets t
JOIN deck_max dm ON dm.deck_id = t.deck_id
ON CONFLICT (note_id, ordinal) DO NOTHING;

-- name: UpdateCardTemplate :execrows
UPDATE cards SET template_id = sqlc.arg(template_id) WHERE id = sqlc.arg(id);

-- Non-locking read for a preview (no FOR UPDATE): used only to compute which ordinals a note-type
-- change would add/remove, before the user has confirmed anything. The actual mutation re-reads
-- via ListCardsForNoteForUpdate inside SyncNoteCards's transaction, so a stale preview here is
-- never a correctness problem -- worst case the confirmation copy is one save behind.
-- name: ListCardsForNote :many
SELECT ordinal FROM cards WHERE note_id = sqlc.arg(note_id) ORDER BY ordinal;

-- Offsets every card backed by these templates to a negative, per-note-unique ordinal so that a
-- subsequent finalize (FinalizeCardOrdinalsForTemplates) can permute ordinals -- including cyclic,
-- not just pairwise -- without transiently violating cards_note_id_ordinal_key. Negative values
-- never collide with a real (>=0) ordinal, touched or not, and the transform x -> -(x+1) is
-- injective, so cards that were already distinct per note stay distinct. Never committed in this
-- shape -- FinalizeCardOrdinalsForTemplates always runs before the transaction commits.
-- name: OffsetCardOrdinalsForTemplates :execrows
UPDATE cards SET ordinal = -(ordinal + 1) WHERE template_id = ANY(sqlc.arg(template_ids)::uuid[]);

-- Sets the real final ordinal for cards staged by OffsetCardOrdinalsForTemplates. Safe as one
-- statement because every affected card is currently negative (from the offset above) and every
-- target is non-negative and, by construction of the caller's plan, unique per note -- so no row's
-- new value can collide with another affected row's current (still-negative) value, or with an
-- untouched card's (already-final, non-negative) value.
-- name: FinalizeCardOrdinalsForTemplates :execrows
WITH m AS (
    SELECT t.template_id, o.new_ordinal
    FROM unnest(sqlc.arg(template_ids)::uuid[]) WITH ORDINALITY AS t(template_id, ord)
    JOIN unnest(sqlc.arg(new_ordinals)::int[]) WITH ORDINALITY AS o(new_ordinal, ord)
      ON t.ord = o.ord
)
UPDATE cards c SET ordinal = m.new_ordinal FROM m WHERE c.template_id = m.template_id;

-- A removed template's cards, across every note of the note type, in one statement (#89). Cascades
-- user_card_state; leaves any review_log rows in place, orphaned but intact -- card_id has no FK
-- (docs/schema.md's Deletion policy), so this is the same, already-decided convention #138 already
-- exercises for a single note's card removal, just applied note-type-wide.
-- name: DeleteCardsForTemplates :execrows
DELETE FROM cards WHERE template_id = ANY(sqlc.arg(template_ids)::uuid[]);

-- internal/http/notetypes.go's #89 structural-change confirmation preview.
-- name: CountCardsForTemplates :one
SELECT count(*) FROM cards WHERE template_id = ANY(sqlc.arg(template_ids)::uuid[]);
