package stream

import (
	"encoding/json"
	"time"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
)

// UIEvent is what a durable frame sends the browser: never the raw payload
// (streaming.md §4.3).
type UIEvent struct {
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	CreatedAt time.Time       `json:"created_at"`
	Payload   json.RawMessage `json:"payload"`
}

// ToUI serializes e for the browser, or reports false if e's type has no
// serializer and must not be sent (streaming.md §4.3 allowlist).
func ToUI(e eventlog.Event) (UIEvent, bool) {
	switch e.Type {
	case eventlog.TypeSessionCreated, eventlog.TypeUserMessage, eventlog.TypeStatusChanged, eventlog.TypeTurnInterrupted:
		return UIEvent{Seq: e.Seq, Type: e.Type, CreatedAt: e.CreatedAt, Payload: e.Payload}, true
	case eventlog.TypeLLMResponse:
		payload, ok := withoutKey(e.Payload, "usage")
		return UIEvent{Seq: e.Seq, Type: e.Type, CreatedAt: e.CreatedAt, Payload: payload}, ok
	default:
		return UIEvent{}, false
	}
}

// withoutKey returns the JSON object payload minus key, or false if payload
// isn't an object.
func withoutKey(payload []byte, key string) (json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, false
	}
	delete(fields, key)
	out, err := json.Marshal(fields)
	return out, err == nil
}
