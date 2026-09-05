-- +goose Up
-- The seventh deck_access permission (#87): read another user's aggregate progress on THIS deck.
-- The one exception to "no permission flag grants read of another user's user_card_state"
-- (docs/schema.md, CLAUDE.md §9) -- deck-scoped, aggregate-only, read-only
-- (docs/plans/87-instructor-dashboard.md §0.3).
-- No last-holder guard: unlike can_manage_access/can_delete, a deck with zero holders of this
-- flag is not stranded, it simply has no instructor view.
ALTER TABLE deck_access ADD COLUMN can_view_progress boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE deck_access DROP COLUMN can_view_progress;
