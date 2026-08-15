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
