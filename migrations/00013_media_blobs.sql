-- +goose Up
-- Metadata only. Bytes live on the filesystem at ${MEDIA_ROOT}/<sha[0:2]>/<sha> -- there is
-- never a bytea column here (architecture.md §12). Deduplicated across ALL users.
CREATE TABLE media_blobs (
    sha256      text        PRIMARY KEY,   -- lowercase hex
    size_bytes  bigint      NOT NULL,
    mime        text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE media_blobs;
