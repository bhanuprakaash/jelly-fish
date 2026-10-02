-- +goose Up
CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE workspaces (
  id         uuid PRIMARY KEY,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
  id           uuid PRIMARY KEY,
  workspace_id uuid NOT NULL REFERENCES workspaces(id),
  email        citext NOT NULL UNIQUE,
  name         text,
  google_sub   text UNIQUE,
  is_admin     boolean NOT NULL DEFAULT false,
  disabled_at  timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE projects (
  id               uuid PRIMARY KEY,
  workspace_id     uuid NOT NULL REFERENCES workspaces(id),
  user_id          uuid NOT NULL REFERENCES users(id),
  name             text NOT NULL,
  instructions     text NOT NULL DEFAULT '',
  default_agent_id uuid,
  use_user_memory  boolean NOT NULL DEFAULT true,
  created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE invites (
  id          uuid PRIMARY KEY,
  email       citext NOT NULL,
  invited_by  uuid NOT NULL REFERENCES users(id),
  expires_at  timestamptz NOT NULL,
  accepted_at timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX invites_open ON invites (email) WHERE accepted_at IS NULL;

CREATE TABLE login_codes (
  id         uuid PRIMARY KEY,
  email      citext NOT NULL,
  code_hash  bytea NOT NULL,
  link_hash  bytea NOT NULL UNIQUE,
  attempts   smallint NOT NULL DEFAULT 0,
  expires_at timestamptz NOT NULL,
  used_at    timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX login_codes_email ON login_codes (email, created_at DESC);

CREATE TABLE login_sessions (
  id           uuid PRIMARY KEY,
  user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash   bytea NOT NULL UNIQUE,
  kind         text NOT NULL DEFAULT 'web' CHECK (kind IN ('web')),
  user_agent   text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL
);
CREATE INDEX login_sessions_user ON login_sessions (user_id);

-- Rows written under the hard-coded dev tenant have no owner to point at.
DELETE FROM usage;
DELETE FROM sessions;

ALTER TABLE sessions
  ADD FOREIGN KEY (workspace_id) REFERENCES workspaces(id),
  ADD FOREIGN KEY (project_id) REFERENCES projects(id),
  ADD FOREIGN KEY (user_id) REFERENCES users(id);
ALTER TABLE usage
  ADD FOREIGN KEY (workspace_id) REFERENCES workspaces(id),
  ADD FOREIGN KEY (project_id) REFERENCES projects(id),
  ADD FOREIGN KEY (user_id) REFERENCES users(id);

-- +goose Down
ALTER TABLE usage
  DROP CONSTRAINT usage_workspace_id_fkey,
  DROP CONSTRAINT usage_project_id_fkey,
  DROP CONSTRAINT usage_user_id_fkey;
ALTER TABLE sessions
  DROP CONSTRAINT sessions_workspace_id_fkey,
  DROP CONSTRAINT sessions_project_id_fkey,
  DROP CONSTRAINT sessions_user_id_fkey;
DROP TABLE login_sessions;
DROP TABLE login_codes;
DROP TABLE invites;
DROP TABLE projects;
DROP TABLE users;
DROP TABLE workspaces;
