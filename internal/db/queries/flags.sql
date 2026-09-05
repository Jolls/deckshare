-- Card-comment feedback from students to the deck owner (#207, migration 00019). Mirrors
-- progress.sql's authorise-and-fetch shape; can_view_flags is deck_access's eighth independent
-- permission, deliberately separate from can_view_progress -- that flag is scoped to
-- aggregate-only reads (#87), and a named student's comment is individual-level disclosure.

-- name: GetDeckForFlags :one
-- can_edit_content is carried alongside so flags.html can decide whether a flagged card links to
-- the note editor, same reason GetDeckForProgress (progress.sql) carries it.
SELECT d.*, da.can_edit_content
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_view_flags
WHERE d.id = sqlc.arg(deck_id);

-- Authorise-and-fetch for the create route: confirms card_id actually belongs to deck_id and the
-- caller can study this deck. Returns just the ids UpsertCardFlag needs.
-- name: GetCardForFlag :one
SELECT c.id, c.deck_id
FROM cards c
JOIN deck_access da ON da.deck_id = c.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_study
WHERE c.id = sqlc.arg(card_id) AND c.deck_id = sqlc.arg(deck_id);

-- Resubmitting while a flag is still open replaces the comment and bumps created_at, rather than
-- piling up a second row -- enforced by card_flags_open_idx (migration 00019).
-- name: UpsertCardFlag :exec
INSERT INTO card_flags (card_id, deck_id, flagged_by_user_id, comment)
VALUES (sqlc.arg(card_id), sqlc.arg(deck_id), sqlc.arg(flagged_by_user_id), sqlc.arg(comment))
ON CONFLICT (card_id, flagged_by_user_id) WHERE status = 'open'
DO UPDATE SET comment = EXCLUDED.comment, created_at = now();

-- Re-checks can_view_flags itself (CLAUDE.md §9: "every query touching a deck takes a user_id
-- and joins deck_access... do not rely on handler-level guards alone") rather than trusting the
-- handler's earlier GetDeckForFlags call -- self-defending the same way GrantDeckAccess/
-- ResolveCardFlag embed their own authorisation. label mirrors ListLapseHotspotsForDeck's
-- (progress.sql) card-label expression -- no rendered-HTML snapshot is stored, so this reads the
-- note's *current* first field, which may already reflect a fix made since the student flagged it.
-- name: ListFlagsForDeck :many
SELECT cf.id, cf.comment, cf.status, cf.created_at, cf.resolved_at,
       u.email AS flagged_by_email, u.display_name AS flagged_by_display_name,
       n.id AS note_id, (n.fields ->> 0)::text AS label
FROM card_flags cf
JOIN users u ON u.id = cf.flagged_by_user_id
JOIN cards c ON c.id = cf.card_id
JOIN notes n ON n.id = c.note_id
JOIN deck_access da ON da.deck_id = cf.deck_id AND da.user_id = sqlc.arg(user_id)
                   AND da.can_view AND da.can_view_flags
WHERE cf.deck_id = sqlc.arg(deck_id) AND cf.status = sqlc.arg(status)
ORDER BY cf.created_at ASC;

-- Feeds the deck page's nav badge (deck.html), the same idea as CountOtherProgressViewers.
-- name: CountOpenFlagsForDeck :one
SELECT count(*) FROM card_flags WHERE deck_id = sqlc.arg(deck_id) AND status = 'open';

-- Authorisation embedded in the join, same shape as GrantDeckAccess. deck_id is bound to the
-- URL's {id} segment (not just derived from the flag), matching how every other URL-scoped
-- mutate route (e.g. access.go's edit/delete) ties every id in the path together, so a flag
-- from a different deck than the URL names 404s instead of silently resolving. Zero rows means
-- the flag doesn't exist, belongs to another deck, is already resolved, or the caller lacks
-- can_view_flags -- all collapsed to 404 by the handler (docs/schema.md).
-- name: ResolveCardFlag :execrows
UPDATE card_flags cf
SET status = 'resolved', resolved_at = now(), resolved_by_user_id = sqlc.arg(resolved_by_user_id)
FROM deck_access da
WHERE cf.id = sqlc.arg(flag_id) AND cf.deck_id = sqlc.arg(deck_id)
  AND da.deck_id = cf.deck_id AND da.user_id = sqlc.arg(resolved_by_user_id)
  AND da.can_view AND da.can_view_flags
  AND cf.status = 'open';
