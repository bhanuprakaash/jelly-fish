-- +goose Up
ALTER TABLE memories DROP CONSTRAINT memories_status_check;
ALTER TABLE memories ADD CONSTRAINT memories_status_check CHECK (status IN ('active','pending_review','deleted'));
ALTER TABLE memories ADD COLUMN deleted_at timestamptz;

-- +goose Down
DELETE FROM memories WHERE status = 'deleted';
ALTER TABLE memories DROP COLUMN deleted_at;
ALTER TABLE memories DROP CONSTRAINT memories_status_check;
ALTER TABLE memories ADD CONSTRAINT memories_status_check CHECK (status IN ('active','pending_review'));
