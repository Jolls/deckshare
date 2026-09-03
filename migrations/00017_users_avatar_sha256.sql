-- +goose Up
-- #176: a user's avatar is a media blob like any other -- metadata row here, bytes on the
-- filesystem at ${MEDIA_ROOT}/<sha[0:2]>/<sha>, deduplicated across all users, never a bytea
-- column (architecture.md §12). RESTRICT for the same reason media_refs uses it (#51): a
-- referenced blob is never deletable, including by the GC sweep, which relies on this FK as its
-- race check (docs/schema.md, Media). NULL = no avatar.
ALTER TABLE users ADD COLUMN avatar_sha256 text
    REFERENCES media_blobs (sha256) ON DELETE RESTRICT;

-- Backs the RESTRICT check above: every media_blobs delete probes this column, so without an
-- index that is a sequential scan of users per blob on every GC tick. Partial because almost
-- every row is NULL, and `avatar_sha256 = $1` implies IS NOT NULL so the probe can still use it.
CREATE INDEX users_avatar_sha256_idx ON users (avatar_sha256) WHERE avatar_sha256 IS NOT NULL;

-- +goose Down
DROP INDEX users_avatar_sha256_idx;
ALTER TABLE users DROP COLUMN avatar_sha256;
