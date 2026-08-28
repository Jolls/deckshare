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

-- name: AppendNoteFieldSlot :execrows
UPDATE notes SET fields = fields || '""'::jsonb, modified_at = now() WHERE note_type_id = $1;
