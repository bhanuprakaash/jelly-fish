package eventlog

import (
	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
)

func sessionCreatedPayload(model string) map[string]any {
	return map[string]any{
		"agent_id": generalAgent(),
		"agent":    map[string]string{"name": "General", "model": model},
		"trigger":  TriggerUserMessage,
	}
}

func userMessagePayload(clientMsgID uuid.UUID, text string) map[string]any {
	return map[string]any{
		"client_msg_id": clientMsgID,
		"message":       msg.UserText(text),
	}
}

// UsageRecorded is the payload of a usage.recorded event (event-log.md §4).
type UsageRecorded struct {
	Kind       string `json:"kind"`
	Provider   string `json:"provider"`
	Model      string `json:"model,omitempty"`
	Quantity   int64  `json:"quantity"`
	Unit       string `json:"unit"`
	CostMicros int64  `json:"cost_micros"`
}

// KindLLM is the usage kind that feeds the session's budget counters.
const KindLLM = "llm"
