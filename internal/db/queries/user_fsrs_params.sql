-- name: GetUserFsrsParams :one
SELECT * FROM user_fsrs_params WHERE id = $1;

-- The per-(user,deck) override if there is one, else the user's global row. deck_id NULLS LAST puts
-- the override first (CLAUDE.md §2.3, §2.4: never one parameter set across a cohort).
-- name: GetEffectiveFsrsParams :one
SELECT fsrs_version, params, desired_retention
FROM user_fsrs_params
WHERE user_id = sqlc.arg(user_id) AND (deck_id = sqlc.arg(deck_id) OR deck_id IS NULL)
ORDER BY deck_id NULLS LAST
LIMIT 1;

-- name: GetGlobalFsrsRetention :one
SELECT desired_retention FROM user_fsrs_params WHERE user_id = $1 AND deck_id IS NULL;

-- Global row upsert targets the partial unique index (migration 00012:
-- user_fsrs_params_user_id_global_key) -- the plain (user_id, deck_id) index does not enforce
-- "one global row per user" because NULLs never conflict against each other in a btree unique
-- index, which is exactly why that second, partial index exists.
-- name: UpsertGlobalFsrsRetention :exec
INSERT INTO user_fsrs_params (user_id, deck_id, fsrs_version, params, desired_retention)
VALUES (sqlc.arg(user_id), NULL, sqlc.arg(fsrs_version), '[]'::jsonb, sqlc.arg(desired_retention))
ON CONFLICT (user_id) WHERE deck_id IS NULL
DO UPDATE SET fsrs_version = EXCLUDED.fsrs_version, desired_retention = EXCLUDED.desired_retention;

-- name: GetDeckFsrsRetention :one
SELECT desired_retention FROM user_fsrs_params WHERE user_id = $1 AND deck_id = $2;

-- Authorisation lives in the SELECT source, not a handler guard (CLAUDE.md §9): a caller
-- without can_view+can_study on the deck matches zero deck_access rows, so the INSERT touches
-- nothing and :execrows reports 0 -- the same "0 rows = not found" shape POST /decks/{id}/edit
-- uses (decks.sql UpdateDeck).
-- name: UpsertDeckFsrsRetention :execrows
INSERT INTO user_fsrs_params (user_id, deck_id, fsrs_version, params, desired_retention)
SELECT sqlc.arg(user_id), sqlc.arg(deck_id), sqlc.arg(fsrs_version), '[]'::jsonb, sqlc.arg(desired_retention)
FROM deck_access da
WHERE da.deck_id = sqlc.arg(deck_id) AND da.user_id = sqlc.arg(user_id) AND da.can_view AND da.can_study
ON CONFLICT (user_id, deck_id)
DO UPDATE SET fsrs_version = EXCLUDED.fsrs_version, desired_retention = EXCLUDED.desired_retention;

-- Seeds a deck's desired retention from an apkg import, first-import-only: DO NOTHING on
-- conflict so a re-import never clobbers a retention the user has since changed themselves.
-- name: SeedDeckFsrsRetention :exec
INSERT INTO user_fsrs_params (user_id, deck_id, fsrs_version, params, desired_retention)
SELECT sqlc.arg(user_id), sqlc.arg(deck_id), sqlc.arg(fsrs_version), '[]'::jsonb, sqlc.arg(desired_retention)
FROM deck_access da
WHERE da.deck_id = sqlc.arg(deck_id) AND da.user_id = sqlc.arg(user_id) AND da.can_view AND da.can_study
ON CONFLICT (user_id, deck_id) DO NOTHING;
