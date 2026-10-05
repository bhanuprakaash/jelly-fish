package memory

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/tool"
)

// write is a checked write command waiting for its transaction.
type write struct {
	args    args
	sess    tool.Session
	project *uuid.UUID
	// path and newPath are a.Path and a.NewPath, cleaned.
	path, newPath string
}

type row struct {
	id                        uuid.UUID
	kind, title, body, status string
	version                   int
}

func (w write) load(ctx context.Context, tx pgx.Tx, p string) (row, bool, error) {
	var r row
	err := tx.QueryRow(ctx, `
		SELECT id, kind, title, content, status, version FROM memories
		WHERE user_id = $1 AND scope = $2 AND project_id IS NOT DISTINCT FROM $3 AND path = $4
		FOR UPDATE`, w.sess.UserID, w.args.Scope, w.project, p).Scan(&r.id, &r.kind, &r.title, &r.body, &r.status, &r.version)
	if errors.Is(err, pgx.ErrNoRows) {
		return row{}, false, nil
	}
	if err != nil {
		return row{}, false, fmt.Errorf("load memory: %w", err)
	}
	return r, true, nil
}

// commit applies the write in tx, which also records the call. It refuses
// with an error Result, writing nothing, when the write breaks a rule.
func (w write) commit(ctx context.Context, tx pgx.Tx) (tool.Result, []tool.Event, error) {
	// One writer per user at a time, so the caps and unique paths hold across
	// sessions.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR NO KEY UPDATE`, w.sess.UserID); err != nil {
		return tool.Result{}, nil, fmt.Errorf("lock user: %w", err)
	}
	cur, found, err := w.load(ctx, tx, w.path)
	if err != nil {
		return tool.Result{}, nil, err
	}
	a := w.args
	status, tainted := statusActive, false
	if w.sess.Tainted {
		status, tainted = statusPending, true
	}

	if a.Command == cmdCreate {
		switch {
		case found && cur.status == statusActive:
			return refuse(w.path + " already exists; use str_replace or insert, or delete it first"), nil, nil
		case found:
			// Overwrites the hidden entry and keeps it hidden; the agent is
			// never told it was there.
			status, tainted = statusPending, true
		}
	} else if !found || cur.status != statusActive {
		return refuse("no memory at " + w.path), nil, nil
	}

	next := row{kind: a.Kind, title: a.Title, body: a.Content}
	newPath := w.path
	switch a.Command {
	case cmdStrReplace:
		switch n := strings.Count(cur.body, a.OldStr); n {
		case 0:
			return refuse("old_str not found in " + w.path), nil, nil
		case 1:
			next.body = strings.Replace(cur.body, a.OldStr, a.NewStr, 1)
		default:
			return refuse(fmt.Sprintf("old_str appears %d times in %s; make it unique", n, w.path)), nil, nil
		}
	case cmdInsert:
		lines := strings.Split(cur.body, "\n")
		if a.InsertLine < 0 || a.InsertLine > len(lines) {
			return refuse(fmt.Sprintf("insert_line must be 0 to %d", len(lines))), nil, nil
		}
		next.body = strings.Join(append(append(lines[:a.InsertLine:a.InsertLine], a.Content), lines[a.InsertLine:]...), "\n")
	case cmdRename:
		newPath, next.body = w.newPath, cur.body
		target, taken, err := w.load(ctx, tx, newPath)
		if err != nil {
			return tool.Result{}, nil, err
		}
		switch {
		case taken && target.status == statusActive:
			return refuse(newPath + " already exists"), nil, nil
		case taken:
			// Like create: replaces the hidden entry and stays hidden.
			if _, err := tx.Exec(ctx, `DELETE FROM memories WHERE id = $1`, target.id); err != nil {
				return tool.Result{}, nil, fmt.Errorf("delete memory: %w", err)
			}
			status, tainted = statusPending, true
		}
	case cmdDelete:
		return w.remove(ctx, tx, cur)
	}
	if a.Command != cmdCreate {
		next.kind, next.title = cmp.Or(next.kind, cur.kind), cmp.Or(next.title, cur.title)
	}
	if len(next.body) > maxContent {
		return refuse(fmt.Sprintf("the memory would be %d bytes; the limit is 4 KB. %s", len(next.body), consolidate)), nil, nil
	}

	why, err := w.checkCaps(ctx, tx, cur.id, found, newPath, next.title, status == statusActive)
	if err != nil {
		return tool.Result{}, nil, err
	}
	if why != "" {
		return refuse(why), nil, nil
	}

	id, version := cur.id, cur.version+1
	if found {
		_, err = tx.Exec(ctx, `
			UPDATE memories SET path = $2, kind = $3, title = $4, content = $5, status = $6, tainted = $7,
			  written_by = 'agent', source_session_id = $8, version = $9, updated_at = now()
			WHERE id = $1`, id, newPath, next.kind, next.title, next.body, status, tainted, w.sess.ID, version)
	} else {
		id, version = uuid.New(), 1
		_, err = tx.Exec(ctx, `
			INSERT INTO memories (id, workspace_id, user_id, scope, project_id, path, kind, title, content, status, tainted, written_by, source_session_id, version)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'agent', $12, $13)`,
			id, w.sess.WorkspaceID, w.sess.UserID, a.Scope, w.project, newPath, next.kind, next.title, next.body, status, tainted, w.sess.ID, version)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO memory_revisions (memory_id, version, path, title, content, written_by, session_id)
			VALUES ($1, $2, $3, $4, $5, 'agent', $6)`, id, version, newPath, next.title, next.body, w.sess.ID)
	}
	if err != nil {
		return tool.Result{}, nil, fmt.Errorf("write memory: %w", err)
	}
	return refRes(id, version, w.ack(newPath)), written(id, newPath, a.Command, version), nil
}

func (w write) ack(newPath string) string {
	switch w.args.Command {
	case cmdCreate:
		return "created " + newPath
	case cmdRename:
		return "renamed " + w.path + " to " + newPath
	}
	return "updated " + newPath
}

func written(id uuid.UUID, p, op string, version int) []tool.Event {
	return []tool.Event{{Type: eventlog.TypeMemoryWritten, Payload: map[string]any{"memory_id": id, "path": p, "op": op, "version": version}}}
}

// remove hard-deletes cur; its revisions go with it.
func (w write) remove(ctx context.Context, tx pgx.Tx, cur row) (tool.Result, []tool.Event, error) {
	if _, err := tx.Exec(ctx, `DELETE FROM memories WHERE id = $1`, cur.id); err != nil {
		return tool.Result{}, nil, fmt.Errorf("delete memory: %w", err)
	}
	return tool.TextResult("deleted "+w.path, false), written(cur.id, w.path, cmdDelete, cur.version), nil
}

// checkCaps returns why a write must not go ahead, or "". id is the row being
// replaced when exists; active is whether the row ends up in the index.
func (w write) checkCaps(ctx context.Context, tx pgx.Tx, id uuid.UUID, exists bool, p, title string, active bool) (string, error) {
	if !exists {
		var n int
		err := tx.QueryRow(ctx, `
			SELECT count(*) FROM memories WHERE user_id = $1 AND scope = $2 AND project_id IS NOT DISTINCT FROM $3`,
			w.sess.UserID, w.args.Scope, w.project).Scan(&n)
		if err != nil {
			return "", fmt.Errorf("count memories: %w", err)
		}
		if n >= maxPerScope {
			return fmt.Sprintf("this scope already holds %d memories, the most allowed. %s", maxPerScope, consolidate), nil
		}
	}
	if !active {
		return "", nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id, path, title FROM memories
		WHERE user_id = $1 AND status = 'active' AND (scope = 'user' OR project_id = $2)`, w.sess.UserID, w.sess.ProjectID)
	if err != nil {
		return "", fmt.Errorf("read index: %w", err)
	}
	defer rows.Close()
	lines, size := 1, len(p)+len(" — ")+len(title)+1
	for rows.Next() {
		var rid uuid.UUID
		var rp, rt string
		if err := rows.Scan(&rid, &rp, &rt); err != nil {
			return "", fmt.Errorf("scan index: %w", err)
		}
		if exists && rid == id {
			continue
		}
		lines++
		size += len(rp) + len(" — ") + len(rt) + 1
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("read index: %w", err)
	}
	if lines > maxIndexLines || size > maxIndexBytes {
		return fmt.Sprintf("the index would pass %d lines or %d KB. %s", maxIndexLines, maxIndexBytes/1024, consolidate), nil
	}
	return "", nil
}
