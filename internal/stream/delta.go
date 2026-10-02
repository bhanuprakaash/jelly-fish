package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	deltaChannel = "jf_stream"
	// maxPayload stays under Postgres's 8000-byte NOTIFY limit.
	maxPayload = 7900
	// maxChunk is the largest text slice tried per NOTIFY before shrinking to
	// fit the escaped JSON in maxPayload.
	maxChunk = 4000
	// subscriberBuffer is how many deltas a slow subscriber may lag before it
	// stops receiving that turn (streaming.md D4).
	subscriberBuffer = 64
	// CoalesceInterval is how often a turn's text is flushed to the bus
	// (event-log.md §5.11).
	CoalesceInterval = 50 * time.Millisecond
)

// Delta kinds.
const (
	// KindText carries reply text.
	KindText = "text"
	// KindReset tells subscribers to drop the turn's text so far: the attempt
	// that produced it failed and another is starting.
	KindReset = "reset"
)

// Delta is one ephemeral fragment of a streaming reply; it is never stored
// (event-log.md §5.11).
type Delta struct {
	// SessionID is omitted from the browser frame.
	SessionID uuid.UUID `json:"session_id,omitzero"`
	TurnID    string    `json:"turn_id"`
	Idx       int       `json:"idx"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text"`
	CallID    string    `json:"call_id,omitempty"`
	Name      string    `json:"name,omitempty"`
}

// PGDeltaBus carries deltas from Workers to Session stream handlers over
// pg_notify('jf_stream'). Publishing needs only the pool; Subscribe needs
// Listen running in the same process.
type PGDeltaBus struct {
	pool *pgxpool.Pool

	mu   sync.Mutex
	subs map[uuid.UUID]map[*subscriber]struct{}
}

// subscriber is one Subscribe call's channel, plus the turns it has lost a
// delta from.
type subscriber struct {
	ch     chan Delta
	gapped map[string]struct{}
}

// NewPGDeltaBus builds a PGDeltaBus over pool.
func NewPGDeltaBus(pool *pgxpool.Pool) *PGDeltaBus {
	return &PGDeltaBus{pool: pool, subs: make(map[uuid.UUID]map[*subscriber]struct{})}
}

// Publish sends d to every subscriber of d.SessionID. Text too long for one
// NOTIFY is split across several, each payload under 8 KB.
func (b *PGDeltaBus) Publish(ctx context.Context, d Delta) error {
	text := d.Text
	for {
		n := min(len(text), maxChunk)
		var payload []byte
		for {
			n = runeBoundary(text, n)
			d.Text = text[:n]
			var err error
			if payload, err = json.Marshal(d); err != nil {
				return fmt.Errorf("marshal delta: %w", err)
			}
			if len(payload) < maxPayload || n <= 1 {
				break
			}
			n /= 2
		}
		if _, err := b.pool.Exec(ctx, "SELECT pg_notify($1, $2)", deltaChannel, string(payload)); err != nil {
			return fmt.Errorf("notify delta: %w", err)
		}
		text = text[n:]
		if text == "" {
			return nil
		}
	}
}

// runeBoundary returns the largest cut <= n that doesn't split a rune, but
// at least one whole rune.
func runeBoundary(s string, n int) int {
	if n >= len(s) {
		return len(s)
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	if n == 0 {
		_, n = utf8.DecodeRuneInString(s)
	}
	return n
}

// Subscribe returns deltas for sid until unsubscribe is called. A subscriber
// that falls behind loses the delta and every later one of that turn, so a
// reply never shows a hole mid-text (streaming.md D4, §5.2).
func (b *PGDeltaBus) Subscribe(sid uuid.UUID) (deltas <-chan Delta, unsubscribe func()) {
	sub := &subscriber{ch: make(chan Delta, subscriberBuffer), gapped: make(map[string]struct{})}

	b.mu.Lock()
	defer b.mu.Unlock()
	set, ok := b.subs[sid]
	if !ok {
		set = make(map[*subscriber]struct{})
		b.subs[sid] = set
	}
	set[sub] = struct{}{}

	return sub.ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subs[sid], sub)
		if len(b.subs[sid]) == 0 {
			delete(b.subs, sid)
		}
	}
}

// deliverPayload routes a jf_stream notification to its session's subscribers
// and returns how many subscribers missed it.
func (b *PGDeltaBus) deliverPayload(payload string) (dropped int, err error) {
	var d Delta
	if err := json.Unmarshal([]byte(payload), &d); err != nil {
		return 0, fmt.Errorf("unmarshal delta: %w", err)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	for sub := range b.subs[d.SessionID] {
		if _, ok := sub.gapped[d.TurnID]; ok {
			dropped++
			continue
		}
		select {
		case sub.ch <- d:
		default:
			sub.gapped[d.TurnID] = struct{}{}
			dropped++
		}
	}
	return dropped, nil
}

// Batcher coalesces a turn's text deltas so the bus sees one publish per
// interval rather than one per token. Publish failures are logged and dropped:
// deltas are best-effort.
type Batcher struct {
	publish  func(context.Context, Delta) error
	sid      uuid.UUID
	turnID   string
	interval time.Duration
	logger   *slog.Logger

	mu  sync.Mutex
	buf strings.Builder
	// pubMu keeps a flush and a Reset from publishing out of order.
	pubMu sync.Mutex
}

// NewBatcher builds a Batcher for one turn that sends batches to publish.
func NewBatcher(publish func(context.Context, Delta) error, sid uuid.UUID, turnID string, interval time.Duration, logger *slog.Logger) *Batcher {
	return &Batcher{publish: publish, sid: sid, turnID: turnID, interval: interval, logger: logger}
}

// Add buffers text for the next flush. It is safe to call while Start's
// flusher runs.
func (b *Batcher) Add(text string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.WriteString(text)
}

// Reset drops text not yet published and publishes a reset delta, so
// subscribers clear the turn's partial reply.
func (b *Batcher) Reset(ctx context.Context) {
	b.pubMu.Lock()
	defer b.pubMu.Unlock()
	b.mu.Lock()
	b.buf.Reset()
	b.mu.Unlock()
	b.send(ctx, Delta{SessionID: b.sid, TurnID: b.turnID, Kind: KindReset})
}

// Start publishes buffered text every interval until ctx is cancelled or stop
// is called. stop waits for the flusher to exit, then publishes the remainder,
// so a turn's text reaches the bus in order and before stop returns.
func (b *Batcher) Start(ctx context.Context) (stop func()) {
	flushCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() {
		ticker := time.NewTicker(b.interval)
		defer ticker.Stop()
		for {
			select {
			case <-flushCtx.Done():
				return
			case <-ticker.C:
				b.flush(ctx)
			}
		}
	})
	return func() {
		cancel()
		wg.Wait()
		b.flush(ctx)
	}
}

func (b *Batcher) flush(ctx context.Context) {
	b.pubMu.Lock()
	defer b.pubMu.Unlock()
	b.mu.Lock()
	text := b.buf.String()
	b.buf.Reset()
	b.mu.Unlock()
	if text == "" {
		return
	}
	b.send(ctx, Delta{SessionID: b.sid, TurnID: b.turnID, Kind: KindText, Text: text})
}

func (b *Batcher) send(ctx context.Context, d Delta) {
	if err := b.publish(ctx, d); err != nil {
		b.logger.Warn("publish delta", "session_id", b.sid, "error", err)
	}
}
