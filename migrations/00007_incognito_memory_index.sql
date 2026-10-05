-- +goose Up
ALTER TABLE sessions ADD COLUMN incognito    boolean NOT NULL DEFAULT false;
ALTER TABLE sessions ADD COLUMN memory_index jsonb;

-- +goose Down
ALTER TABLE sessions DROP COLUMN memory_index;
ALTER TABLE sessions DROP COLUMN incognito;
