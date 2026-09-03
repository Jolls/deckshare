-- name: GetMediaBlob :one
SELECT * FROM media_blobs WHERE sha256 = $1;

-- Dedup is by content, across ALL users (docs/schema.md, Media). ON CONFLICT DO NOTHING because a
-- blob row is immutable once written: size_bytes/mime are pure functions of the bytes the sha256
-- already commits to, so a second import of the same content has nothing new to write.
-- name: CreateMediaBlob :exec
INSERT INTO media_blobs (sha256, size_bytes, mime) VALUES ($1, $2, $3)
ON CONFLICT (sha256) DO NOTHING;

-- The zero-ref half of the media GC sweep (#91, docs/plans/91-orphaned-media-blob-gc.md). A blob
-- outlives the deck that imported it: media_refs cascades away with its deck, media_blobs does not
-- (FK RESTRICT), so a deck delete is the only thing that strands a row here. No age filter -- an
-- import racing to re-reference one of these is handled by the FK, not by waiting (DeleteMediaBlob).
-- Both referencing tables must be checked: users.avatar_sha256 (#176) is a second RESTRICT
-- reference, and the sweep unlinks bytes before deleting the row, so a blob missing from this
-- WHERE clause loses its file before the FK can object.
-- name: ListUnreferencedMediaBlobs :many
SELECT mb.sha256 FROM media_blobs mb
WHERE NOT EXISTS (SELECT 1 FROM media_refs mr WHERE mr.sha256 = mb.sha256)
  AND NOT EXISTS (SELECT 1 FROM users u WHERE u.avatar_sha256 = mb.sha256);

-- Deliberately no NOT EXISTS re-check: Postgres's own RESTRICT enforcement is the check, and it is
-- the only one taken under a lock. A concurrent CreateMediaRef holds FOR KEY SHARE on this row, so
-- a ref created since ListUnreferencedMediaBlobs ran makes this DELETE fail with a foreign-key
-- violation -- which the sweep reads as "someone re-referenced it", skips, and re-lists next tick.
-- name: DeleteMediaBlob :exec
DELETE FROM media_blobs WHERE sha256 = $1;

-- The class-2 half of the GC sweep (#91): which of a batch of on-disk digests still has a row, so
-- the walk can check its whole page of candidates in one round trip instead of one query per file.
-- name: ListExistingMediaBlobs :many
SELECT sha256 FROM media_blobs WHERE sha256 = ANY(sqlc.arg(sha256s)::text[]);

-- Used by GET /media/{sha256} (routes.md): a blob is visible to a user only through a deck they
-- can_view that references it -- the same deck_access join every cross-user read goes through
-- (CLAUDE.md §9). Collapsing "blob doesn't exist" and "blob exists but caller can't see it" into
-- one pgx.ErrNoRows, like GetDeckForUser does, avoids confirming existence to a caller who can't.
-- name: GetMediaBlobForUser :one
SELECT mb.sha256, mb.size_bytes, mb.mime, mb.created_at
FROM media_blobs mb
WHERE mb.sha256 = sqlc.arg(sha256)
  AND EXISTS (
    SELECT 1 FROM media_refs mr
    JOIN deck_access da ON da.deck_id = mr.deck_id
    WHERE mr.sha256 = mb.sha256 AND da.user_id = sqlc.arg(user_id) AND da.can_view
  );
