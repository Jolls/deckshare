-- name: GetMediaBlob :one
SELECT * FROM media_blobs WHERE sha256 = $1;
