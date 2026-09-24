-- +goose Up
-- Operator-issued password reset (#225). Deliberately mirrors `sessions` (migration 00002): the
-- primary key is the SHA-256 hex of the token, so the raw token exists only in the link the
-- operator relays -- a database read discloses nothing usable, and a stolen backup cannot be
-- replayed into an account takeover.
--
-- Not a self-service flow: rows here can only be created by the operator CLI
-- (cmd/reset-password, #225), never by an HTTP request. There is no route that mints one.
CREATE TABLE password_reset_tokens (
    id         text        PRIMARY KEY,  -- SHA-256 hex of the reset token
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,  -- an auth
                                         -- artifact, not content; nothing survives its user,
                                         -- same reasoning as sessions.user_id (#51)
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Purge every outstanding token for one account (issuing a new link, and a successful reset);
-- also backs the CASCADE on user delete.
CREATE INDEX password_reset_tokens_user_id_idx ON password_reset_tokens (user_id);

-- +goose Down
DROP TABLE password_reset_tokens;
