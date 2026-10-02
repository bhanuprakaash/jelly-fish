-- +goose Up
CREATE TABLE provider_keys (
  user_id           uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider          text NOT NULL,
  ciphertext        bytea NOT NULL,
  key_id            text NOT NULL,
  last4             text NOT NULL,
  models            jsonb NOT NULL,
  models_fetched_at timestamptz NOT NULL,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, provider)
);

-- +goose Down
DROP TABLE provider_keys;
