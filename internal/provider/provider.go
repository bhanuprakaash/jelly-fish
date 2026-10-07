// Package provider defines the LLM Provider contract the Worker calls
// (docs/design/provider-gateway.md). Implementations translate the neutral
// message format (ADR 0002) at the edge.
package provider

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
)

// StopReason is why the model stopped (provider-gateway.md §3.3).
type StopReason string

// Stop reasons every adapter maps onto. An unmapped Provider value is
// StopReasonOther.
const (
	StopReasonEndTurn      StopReason = "end_turn"
	StopReasonMaxTokens    StopReason = "max_tokens"
	StopReasonStopSequence StopReason = "stop_sequence"
	StopReasonToolUse      StopReason = "tool_use"
	StopReasonPauseTurn    StopReason = "pause_turn"
	StopReasonRefusal      StopReason = "refusal"
	// StopReasonContextExceeded is a stop, not an error: the loop compacts
	// and retries (provider-gateway.md D10).
	StopReasonContextExceeded StopReason = "context_exceeded"
	StopReasonOther           StopReason = "other"
)

// Request is one model call over the neutral message history.
type Request struct {
	Model string
	// System is the system prompt, one block per entry.
	System   []string
	Messages []msg.Message
	// SummaryEnd is how many leading Messages hold the Compaction summary;
	// the prompt cache keeps a breakpoint after them (context.md §5.2).
	SummaryEnd int
	// NoThinking asks for no extended thinking, for short side calls.
	NoThinking bool
	// Tools the model may call, in a stable order.
	Tools []ToolSpec
}

// ToolSpec is a tool as the model sees it.
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
	Strict      bool            `json:"strict,omitempty"`
}

// DeltaKind says what a Delta carries.
type DeltaKind int

// Delta kinds. Text is the zero value.
const (
	DeltaText DeltaKind = iota
	DeltaThinking
	DeltaToolStart
	DeltaToolArgs
)

// Delta is one streamed fragment of a reply. Opaque bytes (signatures) are
// never streamed; they land only in the final Response.
type Delta struct {
	// Idx is the neutral part index, not the Provider's block index.
	Idx  int
	Kind DeltaKind
	// Text is reply or thinking text, or a partial tool-args JSON fragment.
	Text string
	// CallID is our ToolUse.ID, for tool_start and tool_args.
	CallID string
	// Name is the tool name, for tool_start.
	Name string
}

// Usage counts one call's tokens by billing class. Input excludes cache
// reads and writes, so the classes add up the same for every Provider.
type Usage struct {
	Input        int64
	CacheRead    int64
	CacheWrite5m int64
	CacheWrite1h int64
	Output       int64
	// Reasoning is the part of Output spent thinking; it is not billed twice.
	Reasoning int64
}

// Response is the completed reply to a Request.
type Response struct {
	Message    msg.Message
	StopReason StopReason
	Usage      Usage
	RequestID  string
}

// Provider streams a model reply.
type Provider interface {
	// Name is the provider label recorded in events and usage rows.
	Name() string
	// Stream calls onDelta with each fragment as it arrives, then returns
	// the whole reply. It stops early with ctx's error if ctx ends.
	Stream(ctx context.Context, req Request, onDelta func(Delta)) (Response, error)
}

// ErrKeyRejected means the Provider refused the User's key (401 or 403).
var ErrKeyRejected = errors.New("key rejected")

// Model is one entry of a Provider's live models list.
type Model struct {
	ID             string `json:"id"`
	DisplayName    string `json:"display_name"`
	MaxInputTokens int64  `json:"max_input_tokens,omitempty"`
	MaxTokens      int64  `json:"max_tokens,omitempty"`
}

// Thinking is how a model thinks, which decides the thinking parameter sent.
type Thinking string

// Thinking modes (provider-gateway.md §3.4).
const (
	// ThinkingAdaptiveOnly models take thinking type "adaptive" only.
	ThinkingAdaptiveOnly Thinking = "adaptive_only"
	// ThinkingAlwaysOn models always reason; nothing is sent.
	ThinkingAlwaysOn Thinking = "always_on"
	// ThinkingNone models don't think unless asked with a budget, which we
	// never send (D17).
	ThinkingNone Thinking = "none"
)

// ModelInfo is a Model Catalog entry: limits and capabilities.
type ModelInfo struct {
	ID               string   `json:"id"`
	Provider         string   `json:"provider"`
	ContextWindow    int64    `json:"context_window"`
	MaxOutput        int64    `json:"max_output"`
	Vision           bool     `json:"vision"`
	PDF              bool     `json:"pdf"`
	Structured       bool     `json:"structured_outputs"`
	ForcedToolChoice bool     `json:"forced_tool_choice"`
	Thinking         Thinking `json:"thinking"`
	// Price is nil when the list price is unknown; such a model's usage
	// costs 0 (provider-gateway.md D15).
	Price *Prices `json:"price,omitempty"`
}

// Prices are a model's list prices in USD per million tokens, by Usage
// class, so tokens × price is micro-dollars.
type Prices struct {
	Input        float64 `json:"input"`
	CacheRead    float64 `json:"cache_read"`
	CacheWrite5m float64 `json:"cache_write_5m"`
	CacheWrite1h float64 `json:"cache_write_1h"`
	Output       float64 `json:"output"`
}
