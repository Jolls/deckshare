-- +goose Up
-- #240: before this, cards created in DeckShare never got an import_due_position. They sorted
-- after every imported card, including cards imported later. CreateCard/CreateCardsForNewTemplate
-- now write the deck's max + 1; this gives the cards that already exist the same treatment: per
-- deck, in card-id (uuidv7, creation) order, after the deck's current highest position. Rows that
-- already have a position are not touched. That includes the cards that were not new in Anki at
-- import time, whose position re-import still writes as NULL (UpsertImportedCard). LEAST caps at
-- the integer column's maximum.
WITH deck_max AS (
    SELECT deck_id, COALESCE(max(import_due_position), 0)::bigint AS max_pos
    FROM cards
    GROUP BY deck_id
), numbered AS (
    SELECT c.id,
           LEAST(dm.max_pos + row_number() OVER (PARTITION BY c.deck_id ORDER BY c.id), 2147483647)::integer AS pos
    FROM cards c
    JOIN deck_max dm ON dm.deck_id = c.deck_id
    WHERE c.import_due_position IS NULL
)
UPDATE cards c
SET import_due_position = numbered.pos
FROM numbered
WHERE c.id = numbered.id;

-- +goose Down
-- Irreversible by design: a backfilled position cannot be told apart from an imported one, so
-- Down leaves every value in place.
SELECT 1;
