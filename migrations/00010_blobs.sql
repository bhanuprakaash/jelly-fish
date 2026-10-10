-- +goose Up
CREATE TABLE blobs (
    sha256     text PRIMARY KEY,
    mime       text NOT NULL,
    size       int NOT NULL,
    data       bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
