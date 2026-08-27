-- A deck's creator gets all six flags (docs/schema.md). A personal deck is the trivial case of
-- this, not a separate code path.
-- name: GrantFullDeckAccess :exec
INSERT INTO deck_access (deck_id, user_id, can_view, can_study, can_edit_content,
                         can_edit_settings, can_manage_access, can_delete)
VALUES ($1, $2, true, true, true, true, true, true);
