package stream_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bhanuprakaash/jelly-fish/internal/stream"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

func newMetrics(t *testing.T) *stream.Metrics {
	t.Helper()
	m, err := stream.NewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMetricsExposeExactlyTheStreamSet(t *testing.T) {
	pool := testdb.NewPool(t)
	reg := prometheus.NewRegistry()
	m, err := stream.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.RegisterQueueUsage(t.Context(), reg, pool); err != nil {
		t.Fatal(err)
	}
	m.OpenStreams.WithLabelValues(stream.KindSession).Inc()

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, f := range families {
		var v float64
		switch {
		case f.GetMetric()[0].GetGauge() != nil:
			v = f.GetMetric()[0].GetGauge().GetValue()
		case f.GetMetric()[0].GetCounter() != nil:
			v = f.GetMetric()[0].GetCounter().GetValue()
		}
		got[f.GetName()] = v
	}
	want := map[string]float64{
		"jf_streams_open":                       1,
		"jf_stream_deltas_dropped_total":        0,
		"jf_stream_write_deadline_closes_total": 0,
		"jf_stream_refusals_total":              0,
	}
	// The NOTIFY queue is shared by the whole cluster, so only its range is stable.
	if usage, ok := got["jf_pg_notification_queue_usage"]; !ok || usage < 0 || usage > 1 {
		t.Errorf("jf_pg_notification_queue_usage = %v (present %v), want within [0, 1]", usage, ok)
	}
	delete(got, "jf_pg_notification_queue_usage")
	if len(got) != len(want) {
		t.Fatalf("metrics = %v, want %v", got, want)
	}
	for name, v := range want {
		if g, ok := got[name]; !ok || g != v {
			t.Errorf("%s = %v (present %v), want %v", name, g, ok, v)
		}
	}
}
