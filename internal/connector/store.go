package connector

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/mcpclient"
)

var (
	// ErrSlugTaken is returned when a slug the user chose is already used in
	// the Project.
	ErrSlugTaken = errors.New("slug is taken")
	// ErrNotFound is returned when the Connector, tool or credential does not
	// exist in the Project.
	ErrNotFound = errors.New("not found")
)

// Store keeps Connectors, their cached tools and sealed credentials in
// Postgres. It never sees plaintext.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a Store over pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// ProjectID returns the one Project userID owns.
func (s *Store) ProjectID(ctx context.Context, userID uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT id FROM projects WHERE user_id = $1`, userID).Scan(&id); err != nil {
		return uuid.Nil, fmt.Errorf("find project: %w", err)
	}
	return id, nil
}

// Create stores c with its tools and, if given, its credential in one
// transaction, and returns it with its slug. The slug is c.Slug if set, else
// the letters and digits of the name; a derived slug that is taken gets the
// first free number appended (notion, notion2, ...), a chosen one fails with
// ErrSlugTaken.
func (s *Store) Create(ctx context.Context, c Connector, tools []CachedTool, sealed *Sealed) (Connector, error) {
	chosen := c.Slug != ""
	base := c.Name
	if chosen {
		base = c.Slug
	}
	base = slugify(base)
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for n := 1; ; n++ {
			c.Slug = base
			if n > 1 {
				c.Slug += strconv.Itoa(n)
			}
			tag, err := tx.Exec(ctx, `
				INSERT INTO connectors (id, project_id, slug, name, url, auth_header, protocol_era)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				ON CONFLICT (project_id, slug) DO NOTHING`,
				c.ID, c.ProjectID, c.Slug, c.Name, c.URL, c.AuthHeader, c.Era)
			if err != nil {
				return fmt.Errorf("insert connector: %w", err)
			}
			if tag.RowsAffected() == 1 {
				break
			}
			if chosen {
				return ErrSlugTaken
			}
		}
		for _, t := range tools {
			if err := insertTool(ctx, tx, c.ID, t); err != nil {
				return err
			}
		}
		if sealed == nil {
			return nil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO connector_credentials (connector_id, project_id, access_token, key_id) VALUES ($1, $2, $3, $4)`,
			c.ID, c.ProjectID, sealed.Ciphertext, sealed.KeyID); err != nil {
			return fmt.Errorf("insert credential: %w", err)
		}
		return nil
	})
	if err != nil {
		return Connector{}, err
	}
	return c, nil
}

// slugify keeps the lowercase letters and digits of s.
func slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "connector"
	}
	return b.String()
}

func insertTool(ctx context.Context, tx pgx.Tx, id uuid.UUID, t CachedTool) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO connector_tools (connector_id, name, description, input_schema, annotations, enabled, ttl_ms, tool_hash, fetched_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())`,
		id, t.Name, t.Description, t.InputSchema, nullIfEmpty(t.Annotations), t.Enabled, t.TTLMs, t.Hash)
	if err != nil {
		return fmt.Errorf("insert tool %q: %w", t.Name, err)
	}
	return nil
}

// nullIfEmpty keeps an absent hint NULL rather than an empty JSON value.
func nullIfEmpty(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

// List returns the Project's Connectors and each one's cached tools, enabled
// or not.
func (s *Store) List(ctx context.Context, projectID uuid.UUID) ([]Connector, map[uuid.UUID][]CachedTool, error) {
	return s.load(ctx, projectID, false)
}

// ToolsFor returns the Project's Connectors and each one's enabled tools.
func (s *Store) ToolsFor(ctx context.Context, projectID uuid.UUID) ([]Connector, map[uuid.UUID][]CachedTool, error) {
	return s.load(ctx, projectID, true)
}

func (s *Store) load(ctx context.Context, projectID uuid.UUID, enabledOnly bool) ([]Connector, map[uuid.UUID][]CachedTool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, project_id, slug, name, url, auth_header, protocol_era FROM connectors
		WHERE project_id = $1 ORDER BY created_at, slug`, projectID)
	if err != nil {
		return nil, nil, fmt.Errorf("list connectors: %w", err)
	}
	conns, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Connector, error) {
		var c Connector
		err := row.Scan(&c.ID, &c.ProjectID, &c.Slug, &c.Name, &c.URL, &c.AuthHeader, &c.Era)
		return c, err
	})
	if err != nil {
		return nil, nil, fmt.Errorf("list connectors: %w", err)
	}
	rows, err = s.pool.Query(ctx, `
		SELECT t.connector_id, t.name, t.description, t.input_schema, t.annotations, t.enabled, t.ttl_ms, t.tool_hash, t.fetched_at
		FROM connector_tools t JOIN connectors c ON c.id = t.connector_id
		WHERE c.project_id = $1 AND (t.enabled OR NOT $2) ORDER BY t.name`, projectID, enabledOnly)
	if err != nil {
		return nil, nil, fmt.Errorf("list connector tools: %w", err)
	}
	defer rows.Close()
	tools := map[uuid.UUID][]CachedTool{}
	for rows.Next() {
		var id uuid.UUID
		var t CachedTool
		if err := rows.Scan(&id, &t.Name, &t.Description, &t.InputSchema, &t.Annotations, &t.Enabled, &t.TTLMs, &t.Hash, &t.FetchedAt); err != nil {
			return nil, nil, fmt.Errorf("scan connector tool: %w", err)
		}
		tools[id] = append(tools[id], t)
	}
	return conns, tools, rows.Err()
}

// Delete removes a Connector with its tools and credential, freeing its slug.
func (s *Store) Delete(ctx context.Context, projectID, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM connectors WHERE id = $1 AND project_id = $2`, id, projectID)
	if err != nil {
		return fmt.Errorf("delete connector: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetToolEnabled turns one tool on or off for the model.
func (s *Store) SetToolEnabled(ctx context.Context, projectID, id uuid.UUID, name string, enabled bool) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE connector_tools t SET enabled = $4
		FROM connectors c
		WHERE c.id = t.connector_id AND c.project_id = $1 AND c.id = $2 AND t.name = $3`,
		projectID, id, name, enabled)
	if err != nil {
		return fmt.Errorf("set tool enabled: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Replace overwrites the Connector's cached tools with tools as freshly
// listed. A tool already cached keeps its enabled switch and its hash, so a
// changed tool still carries the hash of what the user approved; a tool no
// longer listed is dropped.
func (s *Store) Replace(ctx context.Context, id uuid.UUID, tools []CachedTool) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		names := make([]string, len(tools))
		for i, t := range tools {
			names[i] = t.Name
			_, err := tx.Exec(ctx, `
				INSERT INTO connector_tools (connector_id, name, description, input_schema, annotations, enabled, ttl_ms, tool_hash, fetched_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
				ON CONFLICT (connector_id, name) DO UPDATE SET
					description = EXCLUDED.description, input_schema = EXCLUDED.input_schema,
					annotations = EXCLUDED.annotations, ttl_ms = EXCLUDED.ttl_ms, fetched_at = now()`,
				id, t.Name, t.Description, t.InputSchema, nullIfEmpty(t.Annotations), t.Enabled, t.TTLMs, t.Hash)
			if err != nil {
				return fmt.Errorf("replace tool %q: %w", t.Name, err)
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM connector_tools WHERE connector_id = $1 AND name <> ALL($2)`, id, names); err != nil {
			return fmt.Errorf("drop unlisted tools: %w", err)
		}
		return nil
	})
}

// Touch marks the Connector's cached tools as fetched now, so a server that
// cannot be listed is tried again after the TTL, not on every drive.
func (s *Store) Touch(ctx context.Context, id uuid.UUID) error {
	if _, err := s.pool.Exec(ctx, `UPDATE connector_tools SET fetched_at = now() WHERE connector_id = $1`, id); err != nil {
		return fmt.Errorf("touch connector tools: %w", err)
	}
	return nil
}

// Credential returns the Connector's sealed credential, or ErrNotFound.
func (s *Store) Credential(ctx context.Context, connectorID uuid.UUID) (Sealed, error) {
	var sealed Sealed
	err := s.pool.QueryRow(ctx, `SELECT access_token, key_id FROM connector_credentials WHERE connector_id = $1`, connectorID).
		Scan(&sealed.Ciphertext, &sealed.KeyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Sealed{}, ErrNotFound
	}
	if err != nil {
		return Sealed{}, fmt.Errorf("get connector credential: %w", err)
	}
	return sealed, nil
}

// SetEra records the protocol era the server answered in.
func (s *Store) SetEra(ctx context.Context, id uuid.UUID, era mcpclient.Era) error {
	if _, err := s.pool.Exec(ctx, `UPDATE connectors SET protocol_era = $2 WHERE id = $1`, id, era); err != nil {
		return fmt.Errorf("set connector era: %w", err)
	}
	return nil
}
