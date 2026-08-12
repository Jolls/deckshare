-- +goose Up
-- Anki references media by filename inside note fields (<img src="x.jpg">), so this mapping
-- is what rendering resolves against. A filename collision within one import is survivable:
-- first-seen wins, with a warning (docs/schema.md §Media) -- enforced by the PK below.
CREATE TABLE media_refs (
    deck_id  uuid NOT NULL REFERENCES decks (id) ON DELETE CASCADE,            -- #51: deck-scoped
                  -- by its own primary key
    filename text NOT NULL,                                                   -- NFC-normalised on read
    sha256   text NOT NULL REFERENCES media_blobs (sha256) ON DELETE RESTRICT, -- #51: a referenced
                  -- blob is never deletable. Orphaned blobs are NOT collected -- deferred, §0.8.

    PRIMARY KEY (deck_id, filename)
);

CREATE INDEX media_refs_sha256_idx ON media_refs (sha256);   -- blob-delete RI + reverse lookup

-- +goose Down
DROP TABLE media_refs;
