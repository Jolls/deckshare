-- A deck's creator gets all six flags (docs/schema.md). A personal deck is the trivial case of
-- this, not a separate code path.
-- name: GrantFullDeckAccess :exec
INSERT INTO deck_access (deck_id, user_id, can_view, can_study, can_edit_content,
                         can_edit_settings, can_manage_access, can_delete)
VALUES ($1, $2, true, true, true, true, true, true);

-- Authorise-and-fetch for the access-management page, the same shape as decks.sql's
-- GetDeckForSettingsEdit. No row means "absent OR invisible OR not permitted" -- deliberately
-- indistinguishable, so a 403 can never become an existence oracle (docs/schema.md).
-- Deliberately no FOR UPDATE: this only authorises a read and the grant insert, neither of which
-- can strand a deck. The paths that can -- revoke and flag change -- take the row lock through
-- LockDeckForAccessChange inside db.RevokeDeckAccess/db.SetDeckAccess (deletion.sql).
-- name: GetDeckForAccessManage :one
SELECT d.*
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_manage_access
WHERE d.id = sqlc.arg(deck_id);

-- The collaborator list for one deck. Authorisation happens in GetDeckForAccessManage above; a
-- caller reaching this has already proved can_manage_access on this deck.
-- name: ListDeckAccessForDeck :many
SELECT u.id AS user_id, u.email, u.display_name,
       da.can_view, da.can_study, da.can_edit_content,
       da.can_edit_settings, da.can_manage_access, da.can_delete
FROM deck_access da
JOIN users u ON u.id = da.user_id
WHERE da.deck_id = sqlc.arg(deck_id)
ORDER BY u.email;

-- Grants a second user access with an explicit choice of flags. The caller's own can_view +
-- can_manage_access is re-verified inside this statement (mirrors LockDeckForAccessChange/
-- UpdateDeck's embedded-authorization shape) rather than trusted from the earlier
-- GetDeckForAccessManage read -- otherwise a caller whose access is revoked between that read and
-- this insert could still grant (#83 review). Zero rows returned means the caller is no longer
-- authorised; the handler treats that the same as GetDeckForAccessManage's 404. No last-holder
-- guard applies here: adding a holder can never strand a deck. A duplicate target raises 23505 on
-- deck_access_pkey, which the handler turns into a 409.
-- name: GrantDeckAccess :execrows
INSERT INTO deck_access (deck_id, user_id, can_view, can_study, can_edit_content,
                         can_edit_settings, can_manage_access, can_delete)
SELECT da.deck_id, sqlc.arg(target_user_id), sqlc.arg(can_view), sqlc.arg(can_study),
       sqlc.arg(can_edit_content), sqlc.arg(can_edit_settings), sqlc.arg(can_manage_access),
       sqlc.arg(can_delete)
FROM deck_access da
WHERE da.deck_id = sqlc.arg(deck_id) AND da.user_id = sqlc.arg(caller_user_id)
  AND da.can_view AND da.can_manage_access;
