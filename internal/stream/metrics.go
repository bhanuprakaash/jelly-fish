package stream

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// Stream kinds, the label values of Metrics.OpenStreams.
const (
	KindSession  = "session"
	KindActivity = "activity"
)

// queueUsageTimeout bounds the pg_notification_queue_usage() query behind a
// scrape.
const queueUsageTimeout = 2 * time.Second

// Metrics are the Session stream's health signals (streaming.md D14).
type Metrics struct {
	// OpenStreams is labelled by stream kind.
	OpenStreams *prometheus.GaugeVec
	// DroppedDeltas counts deltas dropped for a subscriber that fell behind.
	DroppedDeltas prometheus.Counter
	// WriteDeadlineCloses counts streams closed by a missed write deadline.
	WriteDeadlineCloses prometheus.Counter
	// Refusals counts streams refused with 429 by the per-user cap.
	Refusals prometheus.Counter
}

// NewMetrics builds Metrics and registers them on reg.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		OpenStreams: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "jf_streams_open",
			Help: "Open SSE streams on this node.",
		}, []string{"kind"}),
		DroppedDeltas: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "jf_stream_deltas_dropped_total",
			Help: "Deltas dropped for a subscriber that fell behind.",
		}),
		WriteDeadlineCloses: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "jf_stream_write_deadline_closes_total",
			Help: "Streams closed because a write missed its deadline.",
		}),
		Refusals: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "jf_stream_refusals_total",
			Help: "Streams refused with 429 by the per-user cap.",
		}),
	}
	for _, c := range []prometheus.Collector{m.OpenStreams, m.DroppedDeltas, m.WriteDeadlineCloses, m.Refusals} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("register stream metric: %w", err)
		}
	}
	return m, nil
}

// RegisterQueueUsage exports how full Postgres's NOTIFY queue is, from
// pg_notification_queue_usage(), as a gauge on reg. A failed query, or a
// cancelled ctx, reports NaN.
func RegisterQueueUsage(ctx context.Context, reg prometheus.Registerer, pool *pgxpool.Pool) error {
	g := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "jf_pg_notification_queue_usage",
		Help: "Fraction of the Postgres NOTIFY queue in use, 0 to 1.",
	}, func() float64 {
		ctx, cancel := context.WithTimeout(ctx, queueUsageTimeout)
		defer cancel()
		var usage float64
		if err := pool.QueryRow(ctx, "SELECT pg_notification_queue_usage()").Scan(&usage); err != nil {
			return math.NaN()
		}
		return usage
	})
	if err := reg.Register(g); err != nil {
		return fmt.Errorf("register queue usage: %w", err)
	}
	return nil
}
