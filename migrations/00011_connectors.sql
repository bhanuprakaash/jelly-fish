-- +goose Up
CREATE TABLE connectors (
    id           uuid PRIMARY KEY,
    project_id   uuid NOT NULL REFERENCES projects (id),
    slug         text NOT NULL,
    name         text NOT NULL,
    url          text NOT NULL,
    auth_header  text NOT NULL DEFAULT '',
    protocol_era text NOT NULL CHECK (protocol_era IN ('modern', 'legacy')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (project_id, slug)
);

CREATE TABLE connector_tools (
    connector_id uuid NOT NULL REFERENCES connectors (id) ON DELETE CASCADE,
    name         text NOT NULL,
    description  text NOT NULL,
    input_schema jsonb NOT NULL,
    annotations  jsonb,
    enabled      bool NOT NULL DEFAULT true,
    ttl_ms       int,
    tool_hash    text NOT NULL,
    fetched_at   timestamptz NOT NULL,
    PRIMARY KEY (connector_id, name)
);

CREATE TABLE connector_credentials (
    connector_id  uuid PRIMARY KEY REFERENCES connectors (id) ON DELETE CASCADE,
    project_id    uuid NOT NULL,
    issuer        text,
    access_token  bytea NOT NULL,
    refresh_token bytea,
    scopes        text[] NOT NULL DEFAULT '{}',
    key_id        text NOT NULL
);
