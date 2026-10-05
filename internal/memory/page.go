package memory

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
)

// Page changes made by the user on the Memories page or a chat chip. They make
// no LLM call and no event: each one is a memory_revisions row written by
// 'user' with no session (memory.md §5.5).

var (
	// ErrNotFound is returned for a memory or project that is not the caller's.
	ErrNotFound = errors.New("memory not found")
	// ErrChanged is returned by Undo when the memory moved on since the chip.
	ErrChanged = errors.New("memory changed since")
)

// RejectedError is a user edit the rules refuse; Reason is safe to show.
type RejectedError struct{ Reason string }

func (e RejectedError) Error() string { return e.Reason }

// Pages is the Memories page store.
type Pages struct{ pool *pgxpool.Pool }

// NewPages builds the Memories page store over pool.
func NewPages(pool *pgxpool.Pool) *Pages { return &Pages{pool: pool} }

// Item is one memory as the Memories page shows it.
type Item struct {
	ID              uuid.UUID  `json:"id"`
	Scope           string     `json:"scope"`
	Path            string     `json:"path"`
	Title           string     `json:"title"`
	Kind            string     `json:"kind"`
	Content         string     `json:"content"`
	Status          string     `json:"status"`
	Version         int        `json:"version"`
	Stale           bool       `json:"stale"`
	SourceSessionID *uuid.UUID `json:"source_session_id"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// Project is the Project whose Project Memory the page shows.
type Project struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	UseUserMemory bool      `json:"use_user_memory"`
}

// Overview is everything the Memories page lists.
type Overview struct {
	Project  Project `json:"project"`
	Memories []Item  `json:"memories"`
}

// Revision is one saved version of a memory.
type Revision struct {
	Version   int       `json:"version"`
	Path      string    `json:"path"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	WrittenBy string    `json:"written_by"`
	CreatedAt time.Time `json:"created_at"`
}

// List returns the User's Personal project and every memory the page shows:
// User Memory and that Project's Memory, pending ones included. A memory is
// stale when it was last read, or if never read created, over 90 days ago.
func (p *Pages) List(ctx context.Context, scope eventlog.TenantScope) (Overview, error) {
	var o Overview
	err := p.pool.QueryRow(ctx, `
		SELECT id, name, use_user_memory FROM projects
		WHERE workspace_id = $1 AND user_id = $2 AND name = $3`,
		scope.WorkspaceID, scope.UserID, eventlog.PersonalProject).Scan(&o.Project.ID, &o.Project.Name, &o.Project.UseUserMemory)
	if err != nil {
		return Overview{}, fmt.Errorf("load project: %w", err)
	}
	rows, err := p.pool.Query(ctx, `
		SELECT id, scope, path, title, kind, content, status, version,
		  coalesce(last_read_at, created_at) < now() - interval '90 days', source_session_id, updated_at
		FROM memories
		WHERE user_id = $1 AND (scope = 'user' OR project_id = $2)
		ORDER BY scope = 'project', path`, scope.UserID, o.Project.ID)
	if err != nil {
		return Overview{}, fmt.Errorf("list memories: %w", err)
	}
	o.Memories, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Item, error) {
		var m Item
		err := r.Scan(&m.ID, &m.Scope, &m.Path, &m.Title, &m.Kind, &m.Content, &m.Status, &m.Version, &m.Stale, &m.SourceSessionID, &m.UpdatedAt)
		return m, err
	})
	if err != nil {
		return Overview{}, fmt.Errorf("list memories: %w", err)
	}
	if o.Memories == nil {
		o.Memories = []Item{}
	}
	return o, nil
}

// Revisions returns every version of a memory, oldest first.
func (p *Pages) Revisions(ctx context.Context, scope eventlog.TenantScope, id uuid.UUID) ([]Revision, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT r.version, r.path, r.title, r.content, r.written_by, r.created_at
		FROM memory_revisions r JOIN memories m ON m.id = r.memory_id
		WHERE m.id = $1 AND m.user_id = $2
		ORDER BY r.version`, id, scope.UserID)
	if err != nil {
		return nil, fmt.Errorf("list revisions: %w", err)
	}
	revs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Revision])
	if err != nil {
		return nil, fmt.Errorf("list revisions: %w", err)
	}
	if len(revs) == 0 {
		return nil, ErrNotFound
	}
	return revs, nil
}

// Edit saves new content, and a new title when title is non-empty. Editing a
// pending_review memory approves it.
func (p *Pages) Edit(ctx context.Context, scope eventlog.TenantScope, id uuid.UUID, title, content string) error {
	switch {
	case strings.TrimSpace(content) == "":
		return RejectedError{"content is required"}
	case len(content) > maxContent:
		return RejectedError{fmt.Sprintf("content is %d bytes; the limit is 4 KB", len(content))}
	case strings.ContainsAny(title, "\r\n"):
		return RejectedError{"title must be one line"}
	case hasSecret(content, title):
		return RejectedError{"the text looks like a secret or credential. Never store secrets in memory."}
	}
	return pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		cur, path, err := lockOwned(ctx, tx, scope, id)
		if err != nil {
			return err
		}
		return save(ctx, tx, id, cur.version, path, cmp.Or(title, cur.title), content)
	})
}

// Approve makes a pending_review memory active and untainted.
func (p *Pages) Approve(ctx context.Context, scope eventlog.TenantScope, id uuid.UUID) error {
	return pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		cur, path, err := lockOwned(ctx, tx, scope, id)
		if err != nil {
			return err
		}
		if cur.status == statusActive {
			return nil
		}
		return save(ctx, tx, id, cur.version, path, cur.title, cur.body)
	})
}

// Delete hard-deletes a memory; its revisions go with it.
func (p *Pages) Delete(ctx context.Context, scope eventlog.TenantScope, id uuid.UUID) error {
	tag, err := p.pool.Exec(ctx, `DELETE FROM memories WHERE id = $1 AND user_id = $2`, id, scope.UserID)
	if err != nil {
		return fmt.Errorf("delete memory: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Undo reverses the chat write that left the memory at version. A create is
// hard-deleted; any other write is restored to the revision before it. It
// returns ErrChanged when the memory has moved past version.
func (p *Pages) Undo(ctx context.Context, scope eventlog.TenantScope, id uuid.UUID, version int) error {
	return pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		cur, _, err := lockOwned(ctx, tx, scope, id)
		if err != nil {
			return err
		}
		// A pending memory is settled by approve, edit or delete; undo would
		// activate it.
		if cur.version != version || cur.status != statusActive {
			return ErrChanged
		}
		if version == 1 {
			if _, err := tx.Exec(ctx, `DELETE FROM memories WHERE id = $1`, id); err != nil {
				return fmt.Errorf("delete memory: %w", err)
			}
			return nil
		}
		var path, title, content string
		err = tx.QueryRow(ctx, `SELECT path, title, content FROM memory_revisions WHERE memory_id = $1 AND version = $2`, id, version-1).Scan(&path, &title, &content)
		if err != nil {
			return fmt.Errorf("load previous revision: %w", err)
		}
		return save(ctx, tx, id, cur.version, path, title, content)
	})
}

// SetUseUserMemory sets whether new sessions of the project put User Memory
// in their prompt.
func (p *Pages) SetUseUserMemory(ctx context.Context, scope eventlog.TenantScope, projectID uuid.UUID, use bool) error {
	tag, err := p.pool.Exec(ctx, `
		UPDATE projects SET use_user_memory = $3 WHERE id = $1 AND workspace_id = $2 AND user_id = $4`,
		projectID, scope.WorkspaceID, use, scope.UserID)
	if err != nil {
		return fmt.Errorf("set use_user_memory: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// lockOwned loads and locks a memory of scope's user, with its path.
func lockOwned(ctx context.Context, tx pgx.Tx, scope eventlog.TenantScope, id uuid.UUID) (row, string, error) {
	var r row
	var path string
	err := tx.QueryRow(ctx, `
		SELECT id, kind, title, content, status, version, path FROM memories
		WHERE id = $1 AND user_id = $2 FOR UPDATE`, id, scope.UserID).Scan(&r.id, &r.kind, &r.title, &r.body, &r.status, &r.version, &path)
	if errors.Is(err, pgx.ErrNoRows) {
		return row{}, "", ErrNotFound
	}
	if err != nil {
		return row{}, "", fmt.Errorf("load memory: %w", err)
	}
	return r, path, nil
}

// save writes the new state of memory id as the version after from, active
// and untainted.
func save(ctx context.Context, tx pgx.Tx, id uuid.UUID, from int, path, title, content string) error {
	version := from + 1
	_, err := tx.Exec(ctx, `
		UPDATE memories SET path = $2, title = $3, content = $4, status = 'active', tainted = false,
		  written_by = 'user', version = $5, updated_at = now()
		WHERE id = $1`, id, path, title, content, version)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return RejectedError{path + " is used by another memory"}
	}
	if err == nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO memory_revisions (memory_id, version, path, title, content, written_by)
			VALUES ($1, $2, $3, $4, $5, 'user')`, id, version, path, title, content)
	}
	if err != nil {
		return fmt.Errorf("save memory: %w", err)
	}
	return nil
}
