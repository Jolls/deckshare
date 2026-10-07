-- +goose Up
-- #266: newest app version whose release notes this user has seen or dismissed. '' (the default,
-- and every pre-existing account) sorts below every real version, so existing users get the
-- what's-new bar once; new signups are stamped with the running version by CreateUser.
ALTER TABLE users ADD COLUMN last_seen_version text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE users DROP COLUMN last_seen_version;
