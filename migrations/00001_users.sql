-- +goose Up
CREATE TABLE users (
    id             uuid        PRIMARY KEY DEFAULT uuidv7(),  -- application-supplied; DB default is a safety net
    email          text        NOT NULL,
    password_hash  text        NOT NULL,             -- argon2id only; never a weaker algorithm
    display_name   text        NOT NULL,
    timezone       text        NOT NULL DEFAULT 'UTC',   -- IANA name; drives the day boundary
    day_start_hour smallint    NOT NULL DEFAULT 4
        CONSTRAINT users_day_start_hour_check CHECK (day_start_hour BETWEEN 0 AND 23),
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- One account per address, any casing. This index is also what makes a racing duplicate
-- signup a clean 409 rather than two accounts (architecture.md §12).
CREATE UNIQUE INDEX users_email_lower_key ON users (lower(email));

-- +goose Down
DROP TABLE users;
