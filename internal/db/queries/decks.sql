-- name: ListDecksForUser :many
-- #190: is_shared drives the shared-vs-private icon on /decks/ -- a deck is shared once more
-- than one user has a deck_access row on it (the owner's own row is auto-inserted on create).
SELECT d.*, (SELECT count(*) FROM cards c WHERE c.deck_id = d.id) AS card_count, da.can_edit_settings, da.can_edit_content,
       (SELECT count(*) FROM deck_access da2 WHERE da2.deck_id = d.id) > 1 AS is_shared
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id) AND da.can_view
ORDER BY d.name;

-- name: GetDeckForUser :one
SELECT d.*, da.can_edit_content, da.can_edit_settings, da.can_manage_access, da.can_view_progress,
       da.can_view_flags
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id) AND da.can_view
WHERE d.id = sqlc.arg(deck_id);

-- name: ListStudyableDecksForUser :many
-- #169: the decks a mixed "Study All" session may draw from -- can_study, not just can_view (a
-- deck can be shared read-only, which must not let it contribute cards).
SELECT d.*
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
ORDER BY d.name;

-- name: GetDeckForSettingsEdit :one
SELECT d.*, da.can_delete
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

-- The disclosure line on the deck page (#87 §0.4): how many users, other than the viewer, hold
-- can_view_progress on this deck. Not authorised on its own -- the caller already holds can_view
-- via GetDeckForUser before rendering the page this feeds.
-- name: CountOtherProgressViewers :one
SELECT count(*) FILTER (WHERE user_id <> sqlc.arg(user_id)) AS viewer_count
FROM deck_access
WHERE deck_id = sqlc.arg(deck_id) AND can_view_progress;

-- name: CreateDeck :one
INSERT INTO decks (owner_id, name, description) VALUES ($1, $2, $3) RETURNING *;

-- name: UpdateDeck :execrows
UPDATE decks d
SET name = sqlc.arg(name),
    description = sqlc.arg(description),
    -- #101/#115/#116/#118/#154: nested-merge, not jsonb_set. jsonb_set('{}', '{new,perDay}', …, true)
    -- is a no-op when the parent object is missing, which every deck's default '{}' preset is. NULL
    -- leaves the corresponding key untouched so a form that doesn't carry the field can't wipe
    -- the setting. 'new', 'rev', 'priority', and 'due' are independent top-level keys, all patched
    -- off the same original d.preset and merged with a single ||  -- Postgres rejects assigning the
    -- same target column (preset) twice in one SET clause, so this has to be one expression, not
    -- four. Within 'new'/'rev', perDay and order are independently-nullable sub-patches of the same
    -- key; 'priority' and 'due' have only the one field so far, so they skip that extra layer.
    -- priority is top-level rather than nested under 'new' (unlike its predecessor, new.mix) since
    -- it governs the whole day's new/due split (#118), not new-card mixing specifically.
    preset = (CASE WHEN sqlc.narg(new_per_day)::int IS NULL THEN d.preset
                   ELSE d.preset || jsonb_build_object('new',
                          COALESCE(d.preset -> 'new', '{}'::jsonb)
                          || jsonb_build_object('perDay', sqlc.narg(new_per_day)::int))
              END)
           || (CASE WHEN sqlc.narg(rev_per_day)::int IS NULL AND sqlc.narg(rev_order)::text IS NULL THEN '{}'::jsonb
                    ELSE jsonb_build_object('rev',
                           COALESCE(d.preset -> 'rev', '{}'::jsonb)
                           || (CASE WHEN sqlc.narg(rev_per_day)::int IS NULL THEN '{}'::jsonb
                                    ELSE jsonb_build_object('perDay', sqlc.narg(rev_per_day)::int) END)
                           || (CASE WHEN sqlc.narg(rev_order)::text IS NULL THEN '{}'::jsonb
                                    ELSE jsonb_build_object('order', sqlc.narg(rev_order)::text) END))
               END)
           || (CASE WHEN sqlc.narg(priority)::text IS NULL THEN '{}'::jsonb
                    ELSE jsonb_build_object('priority', sqlc.narg(priority)::text)
               END)
           || (CASE WHEN sqlc.narg(due_look_ahead_minutes)::int IS NULL THEN '{}'::jsonb
                    ELSE jsonb_build_object('due',
                           jsonb_build_object('lookAheadMinutes', sqlc.narg(due_look_ahead_minutes)::int))
               END),
    modified_at = now()
FROM deck_access da
WHERE d.id = sqlc.arg(deck_id) AND da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
  AND da.can_view AND da.can_edit_settings;
