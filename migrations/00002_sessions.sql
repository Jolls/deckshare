-- +goose Up
CREATE TABLE sessions (
    id         text        PRIMARY KEY,   -- SHA-256 hex of the session token; the raw token
                                          -- lives only in the cookie, never in the database
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,  -- #51: an auth
                                          -- artifact, not content; nothing survives its user
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Invalidate all of a user's sessions; also backs the CASCADE on user delete (#51).
CREATE INDEX sessions_user_id_idx ON sessions (user_id);

-- +goose Down
DROP TABLE sessions;
