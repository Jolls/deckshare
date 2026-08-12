-- +goose Up
-- The ONLY thing that makes a deck reachable by a second user. Six independent permissions
-- per (user, deck) -- no role enum, no implied hierarchy. can_view being a practical
-- prerequisite for the other five is an application-level convention, deliberately not
-- enforced here (docs/schema.md).
--
-- A deck must always retain at least one can_manage_access holder and one can_delete holder.
-- That guard is enforced in the query layer (internal/db/deletion.go), not here: it is an
-- authorisation rule and it needs a row lock to be race-free, which a constraint trigger would
-- also need (docs/plans/51-deletion-policy.md §0.6).
CREATE TABLE deck_access (
    deck_id           uuid        NOT NULL REFERENCES decks (id) ON DELETE CASCADE,   -- #51: a
                                  -- grant on a deleted deck grants nothing
    user_id           uuid        NOT NULL REFERENCES users (id) ON DELETE RESTRICT,  -- #51: §0.3
    can_view          boolean     NOT NULL DEFAULT false,
    can_study         boolean     NOT NULL DEFAULT false,
    can_edit_content  boolean     NOT NULL DEFAULT false,
    can_edit_settings boolean     NOT NULL DEFAULT false,
    can_manage_access boolean     NOT NULL DEFAULT false,
    can_delete        boolean     NOT NULL DEFAULT false,
    created_at        timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (deck_id, user_id)
);

-- "Decks shared with me", and the RESTRICT RI check on user delete (the PK covers deck_id).
CREATE INDEX deck_access_user_id_idx ON deck_access (user_id);

-- +goose Down
DROP TABLE deck_access;
