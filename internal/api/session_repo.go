package api

import (
	"context"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
)

// SessionRepo is what the session endpoints need from the event log
// (internal/eventlog.Repo satisfies it); defined here since this is where
// it's consumed.
type SessionRepo interface {
	CreateSession(ctx context.Context, scope eventlog.TenantScope, sessionID, clientMsgID uuid.UUID, text, model string, incognito bool) (int64, error)
	PostMessage(ctx context.Context, scope eventlog.TenantScope, sessionID, clientMsgID uuid.UUID, text string) (int64, error)
	SessionLastSeq(ctx context.Context, scope eventlog.TenantScope, sessionID uuid.UUID) (int64, error)
	Interrupt(ctx context.Context, scope eventlog.TenantScope, sessionID uuid.UUID) error
	Retry(ctx context.Context, scope eventlog.TenantScope, sessionID uuid.UUID) error
	ChangeModel(ctx context.Context, scope eventlog.TenantScope, sessionID uuid.UUID, model string) error
	Rename(ctx context.Context, scope eventlog.TenantScope, sessionID uuid.UUID, title string) error
	SessionModel(ctx context.Context, scope eventlog.TenantScope, sessionID uuid.UUID) (string, error)
	ListSessions(ctx context.Context, scope eventlog.TenantScope) ([]eventlog.SessionSummary, error)
	ActivitySnapshot(ctx context.Context, scope eventlog.TenantScope) ([]eventlog.ActivityRow, error)
	ActivityStatusOf(ctx context.Context, scope eventlog.TenantScope, sessionID uuid.UUID) (eventlog.ActivityRow, bool, error)
	ListEvents(ctx context.Context, scope eventlog.TenantScope, sessionID uuid.UUID, after int64) ([]eventlog.Event, error)
}

var _ SessionRepo = (*eventlog.Repo)(nil)
