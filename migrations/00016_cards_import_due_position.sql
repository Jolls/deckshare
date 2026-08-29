-- +goose Up
-- #82: Anki's new-card `due` is a queue-position integer, not a scheduling value -- it has no
-- FSRS meaning and never changes once a card has been studied. Content-ordering metadata, like
-- ordinal, not scheduling state, so it lives on cards without touching invariant §2.1 ("Content
-- addressing ONLY. No due, no ivl, no factor, no state" -- migrations/00009_cards.sql). Null for
-- manually created cards and any card that was never in Anki's New state at import time.
ALTER TABLE cards ADD COLUMN import_due_position integer;

-- +goose Down
ALTER TABLE cards DROP COLUMN import_due_position;
