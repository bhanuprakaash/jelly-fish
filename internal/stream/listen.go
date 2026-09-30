package stream

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	eventsChannel = "jf_events"
	// listenRetryDelay is how long Listen waits before re-LISTENing after a
	// dropped connection (event-log.md §5.7: re-LISTEN, then poll).
	listenRetryDelay = 3 * time.Second
)

// Listen holds one connection LISTENing on jf_events and jf_stream, routing
// hints to hub and deltas to bus, until ctx is cancelled. One connection keeps
// a turn's last delta ahead of its llm.response hint, since a connection
// receives notifications in commit order. On a dropped connection it
// reconnects and re-LISTENs; deltas sent meanwhile are lost, which the stream
// tolerates, and hub subscribers are hinted to catch up (event-log.md §5.7).
func Listen(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, hub *Hub, bus *PGDeltaBus) {
	for ctx.Err() == nil {
		if err := listenOnce(ctx, pool, logger, hub, bus); err != nil && ctx.Err() == nil {
			logger.Warn("listen dropped", "error", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(listenRetryDelay):
		}
	}
}

func listenOnce(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger, hub *Hub, bus *PGDeltaBus) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Release()

	for _, channel := range []string{eventsChannel, deltaChannel} {
		if _, err := conn.Exec(ctx, "LISTEN "+channel); err != nil {
			return fmt.Errorf("listen %s: %w", channel, err)
		}
	}
	hub.hintAll() // catch up on anything missed while disconnected

	for {
		notif, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("wait for notification: %w", err)
		}
		switch notif.Channel {
		case eventsChannel:
			if sid, ok := sessionFromPayload(notif.Payload); ok {
				hub.Notify(sid)
			}
		case deltaChannel:
			if err := bus.deliverPayload(notif.Payload); err != nil {
				logger.Warn("deliver delta", "error", err)
			}
		}
	}
}
