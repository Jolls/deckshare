-- Deck deletion and the deck_access last-holder guard. These are not standalone queries: each
-- one is a step of a transaction orchestrated in internal/db/deletion.go, and running any of
-- them alone is a bug. See docs/plans/51-deletion-policy.md §0.5 and §0.6.

-- Locks the deck row for the duration of the transaction and authorises the caller in one step.
-- No row means "absent OR invisible OR not permitted" -- deliberately indistinguishable, so a
-- 403 can never become an existence oracle (docs/schema.md, Access control).
-- name: LockDeckForDelete :one
SELECT d.id
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
WHERE d.id = sqlc.arg(deck_id)
  AND da.can_view
  AND da.can_delete
FOR UPDATE OF d;

-- Step 2 of DeleteDeck. Deletes every note that this deck delete would leave with no cards at
-- all: notes homed here, and notes homed elsewhere whose cards all live here -- but only when
-- nothing of theirs survives outside this deck. Cascades to cards and user_card_state.
-- review_log is untouched and has no FK to cards (#51 §0.4).
-- name: DeleteNotesOrphanedByDeckDelete :execrows
DELETE FROM notes n
WHERE (
        n.deck_id = sqlc.arg(deck_id)
        OR EXISTS (SELECT 1 FROM cards c WHERE c.note_id = n.id AND c.deck_id = sqlc.arg(deck_id))
      )
  AND NOT EXISTS (SELECT 1 FROM cards c WHERE c.note_id = n.id AND c.deck_id <> sqlc.arg(deck_id));

-- Step 3 of DeleteDeck. Every note still homed here provably has a surviving card elsewhere
-- (the complement of the statement above), so the subquery cannot be NULL -- and if it ever were,
-- the NOT NULL column rejects it loudly. Home deck = the deck of the lowest-ordinal surviving
-- card, which is deterministic and matches architecture.md §20's definition of a home deck.
-- owner_id is re-synced to the new home deck's owner in the same statement: docs/schema.md
-- requires owner_id to track the current deck's owner (it's denormalised, not enforced by a DB
-- constraint), and a re-home is exactly the kind of deck move that must not let it drift.
-- name: RehomeNotesOffDeck :execrows
UPDATE notes n
SET deck_id = (
        SELECT c.deck_id
        FROM cards c
        WHERE c.note_id = n.id AND c.deck_id <> sqlc.arg(deck_id)
        ORDER BY c.ordinal, c.id
        LIMIT 1
    ),
    owner_id = (
        SELECT d.owner_id
        FROM cards c
        JOIN decks d ON d.id = c.deck_id
        WHERE c.note_id = n.id AND c.deck_id <> sqlc.arg(deck_id)
        ORDER BY c.ordinal, c.id
        LIMIT 1
    ),
    modified_at = now()
WHERE n.deck_id = sqlc.arg(deck_id);

-- Step 4 of DeleteDeck. Cascades to cards (and through them to user_card_state), deck_access,
-- per-deck user_fsrs_params, and media_refs. Authorisation happened under the lock above.
-- name: DeleteDeckRow :execrows
DELETE FROM decks WHERE id = sqlc.arg(deck_id);

-- Locks the deck and authorises an access change. Same 404-shaped no-row contract as
-- LockDeckForDelete; the shared lock is what serialises concurrent revocations, without which
-- two callers can each remove "the second-to-last" holder and strand the deck. can_view is
-- required alongside can_manage_access to match GetDeckForAccessManage (deck_access.sql) --
-- otherwise a can_manage_access-without-can_view caller would be 404'd on the access page and on
-- grant but could still edit/revoke other collaborators' access via this path (#83 review).
-- name: LockDeckForAccessChange :one
SELECT d.id
FROM decks d
JOIN deck_access da ON da.deck_id = d.id AND da.user_id = sqlc.arg(user_id)
WHERE d.id = sqlc.arg(deck_id)
  AND da.can_view AND da.can_manage_access
FOR UPDATE OF d;

-- name: DeleteDeckAccessRow :execrows
DELETE FROM deck_access WHERE deck_id = sqlc.arg(deck_id) AND user_id = sqlc.arg(target_user_id);

-- name: UpdateDeckAccessRow :execrows
UPDATE deck_access
SET can_view          = sqlc.arg(can_view),
    can_study         = sqlc.arg(can_study),
    can_edit_content  = sqlc.arg(can_edit_content),
    can_edit_settings = sqlc.arg(can_edit_settings),
    can_manage_access = sqlc.arg(can_manage_access),
    can_delete        = sqlc.arg(can_delete),
    can_view_progress = sqlc.arg(can_view_progress),
    can_view_flags    = sqlc.arg(can_view_flags)
WHERE deck_id = sqlc.arg(deck_id) AND user_id = sqlc.arg(target_user_id);

-- Resets target_user_id's scheduling progress on one deck. review_log is untouched (#189,
-- CLAUDE.md §2.5) -- the next review for a reset card starts fresh because gradeEvent treats a
-- missing user_card_state row as FSRS's zero-value New state (internal/review/grade.go), not as
-- a signal to replay history.
-- name: DeleteUserCardStateForDeck :execrows
DELETE FROM user_card_state ucs
USING cards c
WHERE ucs.card_id = c.id AND c.deck_id = sqlc.arg(deck_id) AND ucs.user_id = sqlc.arg(target_user_id);

-- The guard itself, run AFTER the mutation inside the same transaction. Zero of either count
-- means the deck has been stranded and the caller must roll back.
-- name: CountDeckAccessHolders :one
SELECT
    count(*) FILTER (WHERE can_manage_access) AS manage_count,
    count(*) FILTER (WHERE can_delete)        AS delete_count
FROM deck_access
WHERE deck_id = sqlc.arg(deck_id);
