// Package provider defines the LLM Provider contract the Worker calls
// (docs/design/provider-gateway.md). Implementations translate the neutral
// message format (ADR 0002) at the edge.
package provider

import (
	"context"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
)

// StopReasonEndTurn means the model finished its reply.
const StopReasonEndTurn = "end_turn"

// Request is one model call over the neutral message history.
type Request struct {
	Model    string
	Messages []msg.Message
}

// Usage counts the tokens one call consumed.
type Usage struct {
	InputTokens  int64
	OutputTokens int64
}

// Response is the completed reply to a Request.
type Response struct {
	Message    msg.Message
	StopReason string
	Usage      Usage
}

// Provider streams a model reply.
type Provider interface {
	// Name is the provider label recorded in events and usage rows.
	Name() string
	// Stream calls onDelta with each text fragment as it arrives, then
	// returns the whole reply. It stops early with ctx's error if ctx ends.
	Stream(ctx context.Context, req Request, onDelta func(text string)) (Response, error)
}
