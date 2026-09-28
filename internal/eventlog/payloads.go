package eventlog

import (
	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
)

func sessionCreatedPayload() map[string]any {
	return map[string]any{
		"agent_id": devAgent(),
		"agent":    map[string]string{"name": "General", "model": "fake"},
	}
}

func userMessagePayload(clientMsgID uuid.UUID, text string) map[string]any {
	return map[string]any{
		"client_msg_id": clientMsgID,
		"message":       msg.UserText(text),
	}
}
