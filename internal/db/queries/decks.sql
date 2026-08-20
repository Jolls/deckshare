-- name: GetDeck :one
SELECT * FROM decks WHERE id = $1;

-- name: ListDecksForUser :many
SELECT d.*, (SELECT count(*) FROM cards c WHERE c.deck_id = d.id) AS card_count
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id) AND da.can_view
ORDER BY d.name;

-- name: GetDeckForUser :one
SELECT d.*
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id) AND da.can_view
WHERE d.id = sqlc.arg(deck_id);

-- name: GetDeckForSettingsEdit :one
SELECT d.*
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_edit_settings
WHERE d.id = sqlc.arg(deck_id);

-- name: GetDeckForContentEdit :one
SELECT d.*
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_edit_content
WHERE d.id = sqlc.arg(deck_id);

-- name: CountDeckContents :one
SELECT (SELECT count(*) FROM notes n WHERE n.deck_id = sqlc.arg(deck_id)) AS note_count,
       (SELECT count(*) FROM cards c WHERE c.deck_id = sqlc.arg(deck_id)) AS card_count
FROM deck_access da
WHERE da.deck_id = sqlc.arg(deck_id) AND da.user_id = sqlc.arg(user_id) AND da.can_view;

-- name: CreateDeck :one
INSERT INTO decks (owner_id, name, description) VALUES ($1, $2, $3) RETURNING *;

-- name: UpdateDeck :execrows
UPDATE decks d
SET name = sqlc.arg(name),
    description = sqlc.arg(description),
    -- #101/#115: nested-merge, not jsonb_set. jsonb_set('{}', '{new,perDay}', …, true) is a no-op
    -- when the parent object is missing, which every deck's default '{}' preset is. NULL leaves
    -- preset untouched so a form that doesn't carry the field can't wipe the setting. 'new' and
    -- 'rev' are independent top-level keys, both patched off the same original d.preset and merged
    -- with a single ||  -- Postgres rejects assigning the same target column (preset) twice in one
    -- SET clause, so this has to be one expression, not two.
    preset = (CASE WHEN sqlc.narg(new_per_day)::int IS NULL THEN d.preset
                   ELSE d.preset || jsonb_build_object('new',
                          COALESCE(d.preset -> 'new', '{}'::jsonb)
                          || jsonb_build_object('perDay', sqlc.narg(new_per_day)::int))
              END)
           || (CASE WHEN sqlc.narg(rev_per_day)::int IS NULL THEN '{}'::jsonb
                    ELSE jsonb_build_object('rev',
                           COALESCE(d.preset -> 'rev', '{}'::jsonb)
                           || jsonb_build_object('perDay', sqlc.narg(rev_per_day)::int))
               END),
    modified_at = now()
FROM deck_access da
WHERE d.id = sqlc.arg(deck_id) AND da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
  AND da.can_view AND da.can_edit_settings;
