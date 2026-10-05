-- +goose Up
CREATE TABLE memories (
  id                uuid PRIMARY KEY,
  workspace_id      uuid NOT NULL REFERENCES workspaces(id),
  user_id           uuid NOT NULL REFERENCES users(id),
  scope             text NOT NULL CHECK (scope IN ('user','project')),
  project_id        uuid REFERENCES projects(id) ON DELETE CASCADE,
  path              text NOT NULL,
  kind              text NOT NULL CHECK (kind IN ('preference','fact','feedback','reference')),
  title             text NOT NULL,
  content           text NOT NULL,
  status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active','pending_review')),
  tainted           boolean NOT NULL DEFAULT false,
  written_by        text NOT NULL CHECK (written_by IN ('agent','user','tidy')),
  source_session_id uuid REFERENCES sessions(id) ON DELETE SET NULL,
  version           int NOT NULL DEFAULT 1,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  last_read_at      timestamptz,
  read_count        int NOT NULL DEFAULT 0,
  CHECK ((scope = 'user') = (project_id IS NULL)),
  UNIQUE NULLS NOT DISTINCT (user_id, scope, project_id, path)
);

CREATE TABLE memory_revisions (
  memory_id  uuid NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
  version    int NOT NULL,
  path       text NOT NULL,
  title      text NOT NULL,
  content    text NOT NULL,
  written_by text NOT NULL CHECK (written_by IN ('agent','user','tidy')),
  session_id uuid REFERENCES sessions(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (memory_id, version)
);

-- +goose Down
DROP TABLE memory_revisions;
DROP TABLE memories;
