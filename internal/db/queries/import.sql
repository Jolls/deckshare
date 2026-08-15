-- Import (#58). Every statement here is called only from internal/apkg/dbwrite.go, inside the
-- one transaction an import runs in.

-- name: GetDeckByOwnerAndName :one
SELECT * FROM decks WHERE owner_id = sqlc.arg(owner_id) AND name = sqlc.arg(name);

-- Re-import reuses the owner's deck of that name (docs/schema.md). anki_id is export fidelity
-- only and is never a key -- deck id 1 is "Default" everywhere.
-- name: SetDeckAnkiID :execrows
UPDATE decks SET anki_id = sqlc.narg(anki_id), modified_at = now()
WHERE id = sqlc.arg(deck_id) AND anki_id IS NULL;

-- name: GetNoteTypeByOwnerAndName :one
SELECT * FROM note_types WHERE owner_id = sqlc.arg(owner_id) AND name = sqlc.arg(name);

-- name: CreateImportedNoteType :one
INSERT INTO note_types (owner_id, name, css, is_cloze, sort_field_idx, anki_id)
VALUES (sqlc.arg(owner_id), sqlc.arg(name), sqlc.arg(css), sqlc.arg(is_cloze),
        sqlc.arg(sort_field_idx), sqlc.narg(anki_id))
RETURNING *;

-- The full field row: fields.sql's CreateField carries name only, which would silently drop an
-- imported field's font/size/rtl/sticky.
-- name: CreateImportedField :one
INSERT INTO fields (note_type_id, ordinal, name, font, size, is_rtl, sticky)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING *;

-- Same reason as CreateImportedField: templates.sql's CreateTemplate has no browser formats.
-- name: CreateImportedTemplate :one
INSERT INTO templates (note_type_id, ordinal, name, qfmt, afmt, browser_qfmt, browser_afmt)
VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING *;

-- name: GetNoteByOwnerAndGuid :one
SELECT * FROM notes WHERE owner_id = sqlc.arg(owner_id) AND guid = sqlc.arg(guid);

-- Same deck_access authorisation as notes.sql's CreateNote, plus the imported columns. owner_id
-- comes from the DECK, not the caller (migration 00015's composite FK rejects any other value).
-- name: CreateImportedNote :one
INSERT INTO notes (guid, owner_id, note_type_id, deck_id, fields, tags, checksum,
                   created_at, modified_at, anki_id)
SELECT sqlc.arg(guid), d.owner_id, nt.id, d.id, sqlc.arg(fields), sqlc.arg(tags),
       sqlc.arg(checksum), sqlc.arg(created_at), sqlc.arg(modified_at), sqlc.narg(anki_id)
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_edit_content
JOIN note_types nt ON nt.id = sqlc.arg(note_type_id) AND nt.owner_id = sqlc.arg(user_id)
WHERE d.id = sqlc.arg(deck_id)
RETURNING *;

-- Re-import updates rather than inserts (CLAUDE.md §2.2, apkg-format.md). deck_id is NOT
-- touched: a re-import must not silently move a note the user has since filed elsewhere.
-- name: UpdateImportedNote :execrows
UPDATE notes n
SET fields = sqlc.arg(fields), tags = sqlc.arg(tags), checksum = sqlc.arg(checksum),
    note_type_id = sqlc.arg(note_type_id), modified_at = sqlc.arg(modified_at),
    anki_id = sqlc.narg(anki_id)
FROM deck_access da
WHERE n.id = sqlc.arg(note_id) AND da.deck_id = n.deck_id AND da.user_id = sqlc.arg(user_id)
  AND da.can_view AND da.can_edit_content;

-- cards.deck_id comes from the CARD's own home deck, never flattened to the note's deck
-- (architecture.md §20). ON CONFLICT keeps the existing card id, and with it its
-- user_card_state and review_log history (docs/schema.md's card-regeneration trap).
-- name: UpsertImportedCard :one
INSERT INTO cards (note_id, template_id, ordinal, deck_id, anki_id)
VALUES ($1, $2, $3, $4, sqlc.narg(anki_id))
ON CONFLICT (note_id, ordinal) DO UPDATE
SET deck_id = EXCLUDED.deck_id, template_id = EXCLUDED.template_id, anki_id = EXCLUDED.anki_id
RETURNING *;

-- Imported history. id is omitted so the column DEFAULT uuidv7() supplies it -- an imported row
-- has no client-generated id and this package must not grow a UUID dependency to invent one.
-- stability_before / difficulty_before / fsrs_version stay NULL: Anki's revlog carries SM-2
-- values, and writing a fabricated FSRS prior would be permanently wrong training data
-- (CLAUDE.md §2.5). ON CONFLICT DO NOTHING makes a re-import a no-op on the dedup key.
-- name: InsertImportedReviewLog :execrows
INSERT INTO review_log (
    user_id, card_id, rating, reviewed_at, duration_ms,
    state_before, learning_steps_before, elapsed_days_before, scheduled_days_after,
    review_kind, anki_id
) VALUES (
    sqlc.arg(user_id), sqlc.arg(card_id), sqlc.arg(rating), sqlc.arg(reviewed_at),
    sqlc.narg(duration_ms), sqlc.arg(state_before), sqlc.arg(learning_steps_before),
    sqlc.arg(elapsed_days_before), sqlc.arg(scheduled_days_after),
    sqlc.arg(review_kind), sqlc.narg(anki_id)
)
ON CONFLICT (user_id, card_id, anki_id) DO NOTHING;

-- The seed path for a card that arrives with scheduling state but no review history. DO NOTHING,
-- not DO UPDATE: an existing row is this user's own live progress and an imported snapshot never
-- outranks it.
-- name: SeedImportedUserCardState :execrows
INSERT INTO user_card_state (user_id, card_id, due, stability, difficulty, state, reps, lapses,
                             elapsed_days, scheduled_days, learning_steps, last_review,
                             suspended, flag)
VALUES (sqlc.arg(user_id), sqlc.arg(card_id), sqlc.arg(due), sqlc.arg(stability),
        sqlc.arg(difficulty), sqlc.arg(state), sqlc.arg(reps), sqlc.arg(lapses),
        sqlc.arg(elapsed_days), sqlc.arg(scheduled_days), sqlc.arg(learning_steps),
        sqlc.narg(last_review), sqlc.arg(suspended), sqlc.arg(flag))
ON CONFLICT (user_id, card_id) DO NOTHING;
