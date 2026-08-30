-- Cards are content addressing only -- no scheduling columns exist here to lose (CLAUDE.md §2.1).
-- These four statements are the whole of card regeneration; see internal/db/cards.go for the
-- diff that calls them and docs/schema.md's card-regeneration trap for why it is a diff.

-- name: ListCardsForNoteForUpdate :many
SELECT id, ordinal, template_id, deck_id FROM cards WHERE note_id = $1 ORDER BY ordinal FOR UPDATE;

-- Called once per card in the create set (§0.3's "create" batch is small -- at most one row
-- per template/cloze ordinal) rather than as a single multi-row statement: sqlc's query
-- analyzer cannot resolve a two-array unnest(...) without a live database catalog.
-- name: CreateCard :one
INSERT INTO cards (note_id, template_id, ordinal, deck_id) VALUES ($1, $2, $3, $4) RETURNING *;

-- name: DeleteCardsByOrdinals :execrows
DELETE FROM cards WHERE note_id = sqlc.arg(note_id) AND ordinal = ANY(sqlc.arg(ordinals)::int[]);

-- One card per existing note when a template is appended to a non-cloze note type (#54 §0.5).
-- Filed in each note's home deck: notes.deck_id is the default for cards generated later
-- (architecture.md §20).
-- name: CreateCardsForNewTemplate :execrows
INSERT INTO cards (note_id, template_id, ordinal, deck_id)
SELECT n.id, sqlc.arg(template_id), sqlc.arg(ordinal), n.deck_id
FROM notes n WHERE n.note_type_id = sqlc.arg(note_type_id)
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
