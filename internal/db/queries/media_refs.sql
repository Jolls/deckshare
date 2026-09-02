-- name: GetMediaRef :one
SELECT * FROM media_refs WHERE deck_id = $1 AND filename = $2;

-- No deck_access join (CLAUDE.md §9): callers are internal/review's renderQueueRows, for a deck
-- the review handler has already authorised with GetDeckForStudy (can_view AND can_study), and
-- internal/http's note-preview handlers (note_preview.go), each of which has already authorised
-- the same deck via GetNoteForContentEdit / GetDeckForContentEdit before calling this.
-- name: ListMediaRefsForDeck :many
SELECT * FROM media_refs WHERE deck_id = $1;

-- First-seen-wins (docs/schema.md, Media): within one import collectMedia already resolves a
-- same-name collision before this is ever called; ON CONFLICT DO NOTHING extends the same policy
-- across re-imports -- the first ref a name ever got, on that deck, is the one kept.
-- name: CreateMediaRef :exec
INSERT INTO media_refs (deck_id, filename, sha256) VALUES ($1, $2, $3)
ON CONFLICT (deck_id, filename) DO NOTHING;
