-- name: GetMediaBlob :one
SELECT * FROM media_blobs WHERE sha256 = $1;

-- Dedup is by content, across ALL users (docs/schema.md, Media). ON CONFLICT DO NOTHING because a
-- blob row is immutable once written: size_bytes/mime are pure functions of the bytes the sha256
-- already commits to, so a second import of the same content has nothing new to write.
-- name: CreateMediaBlob :exec
INSERT INTO media_blobs (sha256, size_bytes, mime) VALUES ($1, $2, $3)
ON CONFLICT (sha256) DO NOTHING;

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
