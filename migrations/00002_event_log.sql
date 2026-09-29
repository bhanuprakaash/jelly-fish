-- +goose Up
CREATE TABLE sessions (
  id                uuid PRIMARY KEY,
  workspace_id      uuid NOT NULL,
  project_id        uuid NOT NULL,
  user_id           uuid NOT NULL,
  parent_id         uuid REFERENCES sessions(id) ON DELETE CASCADE,
  depth             smallint NOT NULL DEFAULT 0,
  agent_id          uuid NOT NULL,
  status            text NOT NULL CHECK (status IN ('runnable','running','awaiting_approval',
                      'awaiting_user','awaiting_children','sleeping','completed','failed')),
  last_seq          bigint NOT NULL DEFAULT 0,
  ready_at          timestamptz,
  wake_at           timestamptz,
  lease_owner       text,
  lease_epoch       bigint NOT NULL DEFAULT 0,
  lease_expires_at  timestamptz,
  cancel_requested  boolean NOT NULL DEFAULT false,
  background        boolean NOT NULL DEFAULT false,
  recovery_attempts int NOT NULL DEFAULT 0,
  tokens_used       bigint NOT NULL DEFAULT 0,
  cost_micros       bigint NOT NULL DEFAULT 0,
  turns             int    NOT NULL DEFAULT 0,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_runnable ON sessions (ready_at) WHERE status = 'runnable';
CREATE INDEX sessions_sleeping ON sessions (wake_at) WHERE status = 'sleeping';
CREATE INDEX sessions_expired  ON sessions (lease_expires_at) WHERE status = 'running';
CREATE INDEX sessions_project  ON sessions (workspace_id, project_id, updated_at DESC);
CREATE INDEX sessions_parent   ON sessions (parent_id) WHERE parent_id IS NOT NULL;

CREATE TABLE events (
  session_id     uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  seq            bigint NOT NULL,
  workspace_id   uuid NOT NULL,
  type           text NOT NULL,
  schema_version smallint NOT NULL DEFAULT 1,
  actor          text NOT NULL,
  causation_seq  bigint,
  correlation_id text,
  lease_epoch    bigint,
  payload        jsonb NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (session_id, seq)
);
CREATE UNIQUE INDEX events_client_msg ON events (session_id, (payload->>'client_msg_id'))
  WHERE type = 'user.message';

CREATE TABLE usage (
  id           bigserial PRIMARY KEY,
  session_id   uuid,
  seq          bigint,
  workspace_id uuid NOT NULL,
  project_id   uuid NOT NULL,
  user_id      uuid NOT NULL,
  kind         text NOT NULL,
  provider     text NOT NULL,
  model        text,
  quantity     numeric NOT NULL,
  unit         text NOT NULL,
  cost_micros  bigint,
  created_at   timestamptz NOT NULL,
  UNIQUE (session_id, seq)
);

-- +goose Down
DROP TABLE usage;
DROP TABLE events;
DROP TABLE sessions;
