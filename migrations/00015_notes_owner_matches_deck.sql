-- +goose Up
-- #54: notes.owner_id is denormalised from decks.owner_id (docs/schema.md, Identifiers) because
-- UNIQUE (owner_id, guid) -- the import idempotency key -- cannot span a join. Until now nothing
-- enforced the equality at the database level; a composite FK does, declaratively. CHECK cannot
-- subquery another table and a generated column cannot reference another row, so this is the only
-- non-procedural mechanism Postgres offers for this shape.
ALTER TABLE decks ADD CONSTRAINT decks_id_owner_id_key UNIQUE (id, owner_id);

ALTER TABLE notes ADD CONSTRAINT notes_deck_id_owner_id_fkey
    FOREIGN KEY (deck_id, owner_id) REFERENCES decks (id, owner_id)
    ON UPDATE RESTRICT ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE notes DROP CONSTRAINT notes_deck_id_owner_id_fkey;
ALTER TABLE decks DROP CONSTRAINT decks_id_owner_id_key;
