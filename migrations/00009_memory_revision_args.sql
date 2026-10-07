-- +goose Up
ALTER TABLE memory_revisions ADD COLUMN args jsonb;

-- +goose Down
ALTER TABLE memory_revisions DROP COLUMN args;
