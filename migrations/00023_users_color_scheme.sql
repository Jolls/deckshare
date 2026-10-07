-- +goose Up
-- #268: per-user colour scheme. 'auto' (default) renders no data-theme attribute, so Pico follows
-- prefers-color-scheme; 'light'/'dark' are rendered server-side as <html data-theme="..."> so
-- first paint is correct. Allowlist must match colorSchemes in internal/http/settings.go.
ALTER TABLE users ADD COLUMN color_scheme text NOT NULL DEFAULT 'auto'
    CONSTRAINT users_color_scheme_check CHECK (color_scheme IN ('auto', 'light', 'dark'));

-- +goose Down
ALTER TABLE users DROP COLUMN color_scheme;
