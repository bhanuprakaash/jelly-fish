package eventlog

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
)

// placeholderRunes is how much of the first user message stands in for a
// missing Title.
const placeholderRunes = 60

// SessionSummary is one row of the Chat List.
type SessionSummary struct {
	ID        uuid.UUID
	Title     string // the session's Title, else a placeholder from its first user message
	Titled    bool   // whether Title is set rather than a placeholder
	Status    string
	UpdatedAt time.Time
}

// ListSessions returns the User's top-level, non-System sessions, most
// recently active first (streaming.md D20).
func (r *Repo) ListSessions(ctx context.Context, scope TenantScope) ([]SessionSummary, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT s.id, s.title, s.status, s.updated_at, f.payload
		FROM sessions s
		LEFT JOIN LATERAL (
			SELECT e.payload FROM events e
			WHERE e.session_id = s.id AND e.type = $4
			ORDER BY e.seq LIMIT 1) f ON s.title IS NULL
		WHERE s.workspace_id = $1 AND s.user_id = $2
		  AND s.parent_id IS NULL AND s.trigger = $3
		ORDER BY s.updated_at DESC, s.id`,
		scope.WorkspaceID, scope.UserID, TriggerUserMessage, TypeUserMessage)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SessionSummary, error) {
		var (
			s     SessionSummary
			title *string
			first []byte
		)
		if err := row.Scan(&s.ID, &title, &s.Status, &s.UpdatedAt, &first); err != nil {
			return s, err
		}
		if title != nil {
			s.Title, s.Titled = *title, true
			return s, nil
		}
		if first == nil {
			return s, nil
		}
		var p struct {
			Message msg.Message `json:"message"`
		}
		if err := json.Unmarshal(first, &p); err != nil {
			return s, fmt.Errorf("session %s first message: %w", s.ID, err)
		}
		s.Title = placeholder(p.Message.Text())
		return s, nil
	})
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return out, nil
}

func placeholder(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > placeholderRunes {
		return string(r[:placeholderRunes]) + "…"
	}
	return text
}
