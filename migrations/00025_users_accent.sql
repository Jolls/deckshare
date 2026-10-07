-- +goose Up
-- #267: per-user accent colour, one of Pico's stock palettes. 'azure' is Pico's default and renders
-- no data-accent attribute, so existing users are unchanged. Allowlist must match accentOptions in
-- internal/http/settings.go and accents in scripts/gen-accents.
ALTER TABLE users ADD COLUMN accent text NOT NULL DEFAULT 'azure'
    CONSTRAINT users_accent_check CHECK (accent IN ('azure', 'blue', 'indigo', 'purple', 'pink', 'red', 'orange', 'green'));

-- +goose Down
ALTER TABLE users DROP COLUMN accent;
