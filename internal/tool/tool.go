// Package tool defines the Tool contract the Worker runs between model turns
// (docs/design/agent-loop.md §4.2).
package tool

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bhanuprakaash/jelly-fish/internal/mcpclient"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
)

// DefaultTimeout bounds a call whose Def sets none (agent-loop.md D11).
const DefaultTimeout = 120 * time.Second

// Def describes a Tool to the model and to the Worker.
type Def struct {
	Name, Description string
	Schema            json.RawMessage
	// ParallelSafe calls may run alongside each other, at most 4 at once.
	ParallelSafe bool
	// Untrusted output taints memory writes after it (memory.md §5.2).
	Untrusted bool
	// Timeout bounds one call; zero means DefaultTimeout.
	Timeout time.Duration
	// Strict asks the Provider to enforce Schema exactly. Only a schema in the
	// intersection dialect may set it (provider-gateway.md §4.5).
	Strict bool
	// Source is "connector:<id>" for a Connector's tool; empty means built in.
	Source string
	// ReadOnly and Destructive are the tool's hints, set only when trusted.
	ReadOnly, Destructive bool
}

// CallInput is one call's arguments. IdempotencyKey is stable for the call
// across crashes, for tools that can use one (event-log.md §5.5).
type CallInput struct {
	CallID, IdempotencyKey string
	Args                   json.RawMessage
	Session                Session
	// Resume carries the User's answer when the call retries after returning
	// InputRequired; Args stay the original.
	Resume *Resume
	// Elicit asks the User during the call and blocks for the answer; Progress
	// reports how far the call is. Either may be nil.
	Elicit   func(ctx context.Context, req InputRequest) (InputResponse, error)
	Progress func(message string, progress, total float64)
}

// InputRequired is what a call needs answered before it can finish.
type InputRequired = mcpclient.InputRequired

// InputRequest is one question of an InputRequired, or a legacy elicitation.
type InputRequest = mcpclient.InputRequest

// InputResponse is the User's answer to one InputRequest.
type InputResponse = mcpclient.InputResponse

// Resume is the answer a call retries with.
type Resume = mcpclient.ResumeMeta

// Session is the session a call runs for.
type Session struct {
	ID, UserID, WorkspaceID, ProjectID uuid.UUID
	// Child is set for a Child Session.
	Child bool
	// Tainted is set when an untrusted tool's output arrived since the last
	// user message (memory.md §5.2).
	Tainted bool
}

// Result is what the model sees from a call.
type Result struct {
	Content []msg.Part
	IsError bool
	// InputRequired is set when the call cannot finish before the User answers.
	// The Worker parks the session, and the call runs again with Resume.
	InputRequired *InputRequired
	// Commit, if set, runs in the transaction that records the call's
	// completion, so the call's database writes land with it or not at all.
	// The Result it returns replaces this one, and its Events are appended
	// before the completion.
	Commit func(ctx context.Context, tx pgx.Tx) (Result, []Event, error)
}

// Event is an Event Log entry a Commit adds.
type Event struct {
	Type    string
	Payload any
}

// Tool is one callable tool. Call must return soon after ctx ends: a call
// past its Timeout is only recorded once it does. An error from Call means a
// bug or broken infrastructure; the Worker reports it to the model as an
// error Result.
type Tool interface {
	Def() Def
	Call(ctx context.Context, in CallInput) (Result, error)
}

// ParallelByArgs is implemented by a Tool whose calls differ in whether they
// may run alongside each other; it overrides Def.ParallelSafe.
type ParallelByArgs interface {
	ParallelSafeCall(args json.RawMessage) bool
}

// Registry holds the tools a session can call. A nil Registry has none.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry builds a Registry of tools, keyed by their names.
func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{tools: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		r.tools[t.Def().Name] = t
	}
	return r
}

// With returns a Registry of r's tools and tools; on a name clash r's tool wins.
func (r *Registry) With(tools ...Tool) *Registry {
	n := NewRegistry(tools...)
	if r != nil {
		maps.Copy(n.tools, r.tools)
	}
	return n
}

// Defs lists the tools by name, so the list sent to the model is stable.
func (r *Registry) Defs() []Def {
	if r == nil {
		return []Def{}
	}
	names := slices.Sorted(maps.Keys(r.tools))
	defs := make([]Def, len(names))
	for i, n := range names {
		defs[i] = r.tools[n].Def()
	}
	return defs
}

// Hash identifies a tool list, so a turn records which one it ran with.
func Hash(defs []Def) (string, error) {
	b, err := json.Marshal(defs)
	if err != nil {
		return "", fmt.Errorf("marshal tool list: %w", err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(b)), nil
}

// Get returns the tool called name.
func (r *Registry) Get(name string) (Tool, bool) {
	if r == nil {
		return nil, false
	}
	t, ok := r.tools[name]
	return t, ok
}

// TextResult is a Result holding one text part.
func TextResult(text string, isError bool) Result {
	return Result{Content: []msg.Part{{Kind: msg.KindText, Text: text}}, IsError: isError}
}
