-- +goose Up
ALTER TABLE sessions
  ADD COLUMN trigger text NOT NULL DEFAULT 'user_message'
    CHECK (trigger IN ('user_message','memory_tidy')),
  ADD COLUMN title text;

-- +goose Down
ALTER TABLE sessions DROP COLUMN title, DROP COLUMN trigger;
