package worker_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bhanuprakaash/jelly-fish/internal/auth"
	"github.com/bhanuprakaash/jelly-fish/internal/connector"
	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/mcpclient"
	"github.com/bhanuprakaash/jelly-fish/internal/mcpclient/mcptest"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
	"github.com/bhanuprakaash/jelly-fish/internal/tool"
)

// resolveLatest answers the newest approval.requested of the session.
func resolveLatest(t *testing.T, pool *pgxpool.Pool, scope eventlog.TenantScope, sid uuid.UUID, a eventlog.Answer) {
	t.Helper()
	asked := ofType(loadEvents(t, pool, sid), eventlog.TypeApprovalRequested)
	approvalID, err := uuid.Parse(asked[len(asked)-1].Payload["approval_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if err := eventlog.NewRepo(pool, "fake").ResolveApproval(t.Context(), scope, sid, approvalID, a); err != nil {
		t.Fatal(err)
	}
}

// callsOf is a reply that calls each tool once, as c1, c2, ...
func callsOf(names ...string) provider.Response {
	m := msg.AssistantText("on it")
	for i, name := range names {
		m.Parts = append(m.Parts, msg.Part{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: "c" + string(rune('1'+i)), Name: name, Args: json.RawMessage(`{}`)}})
	}
	return provider.Response{Message: m, StopReason: provider.StopReasonToolUse}
}

// approvalRun is a session of a Worker that offers the built-in tool-a and
// the Notion connector's tools x, y and z.
type approvalRun struct {
	*connectorEnv
	p       *replies
	builtin tool.Tool
	user    auth.User
	connID  uuid.UUID
	sid     uuid.UUID
}

func newApprovalRun(t *testing.T, list ...provider.Response) *approvalRun {
	t.Helper()
	p := &replies{list: list}
	builtin := fn{id: "a", call: func(context.Context) tool.Result { return tool.TextResult("a ran", false) }}
	e := newConnectorEnv(t, p, builtin)
	srv := mcptest.Start(t, mcptest.Options{})
	for _, name := range []string{"x", "y", "z"} {
		srv.AddTool(name, &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok " + name}}})
	}
	user := testdb.NewUser(t, e.pool)
	r := &approvalRun{connectorEnv: e, p: p, builtin: builtin, user: user, connID: e.addConnector(t, user.ID, srv, time.Minute)}
	r.cacheTools(t, "")
	return r
}

// cacheTools replaces the cached tools with x, y and z; a description for x
// makes it a changed tool, since a cached tool keeps the hash the User approved.
func (r *approvalRun) cacheTools(t *testing.T, xDescription string) {
	t.Helper()
	var tools []connector.CachedTool
	for _, name := range []string{"x", "y", "z"} {
		raw := mcpclient.RawTool{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`)}
		if name == "x" {
			raw.Description = xDescription
		}
		tools = append(tools, connector.FromRaw(raw))
	}
	if err := r.store.Replace(t.Context(), r.connID, tools); err != nil {
		t.Fatal(err)
	}
}

func (r *approvalRun) begin(t *testing.T) {
	t.Helper()
	r.sid = r.newSession(t, r.user.Scope())
}

func (r *approvalRun) resolve(t *testing.T, a eventlog.Answer) {
	t.Helper()
	resolveLatest(t, r.pool, r.user.Scope(), r.sid, a)
}

// awaitCard waits until the nth approval.requested is open and returns its payload.
func (r *approvalRun) awaitCard(t *testing.T, n int) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		if err := r.pool.QueryRow(t.Context(), `SELECT status FROM sessions WHERE id = $1`, r.sid).Scan(&status); err != nil {
			t.Fatal(err)
		}
		asked := ofType(loadEvents(t, r.pool, r.sid), eventlog.TypeApprovalRequested)
		if status == eventlog.StatusAwaitingApproval && len(asked) == n {
			return asked[n-1].Payload
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("approval card %d never opened", n)
	return nil
}

// resultTexts is what the model saw as each result of its first batch.
func (r *approvalRun) resultTexts() []string {
	var out []string
	for _, part := range r.p.requests()[1].Messages[2].Parts {
		out = append(out, part.ToolResult.Parts[0].Text)
	}
	return out
}

func TestEveryConnectorToolAsksBeforeAnyCallStarts(t *testing.T) {
	r := newApprovalRun(t, callsOf("tool-a", "notion__x", "notion__y"), done())
	r.begin(t)

	card := r.awaitCard(t, 1)
	evs := loadEvents(t, r.pool, r.sid)
	var asks []any
	for _, e := range ofType(evs, eventlog.TypeToolRequested) {
		asks = append(asks, e.Payload["ask"])
	}
	if !slices.Equal(asks, []any{false, true, true}) {
		t.Fatalf("ask flags = %v, want [false true true]", asks)
	}
	if card["tool_call_id"] != "c2" || card["tool"] != "notion__x" || card["connector"] != "Notion" || card["kind"] != "tool" {
		t.Fatalf("first card = %v, want the tool call c2 of connector Notion", card)
	}
	if n := len(ofType(evs, eventlog.TypeToolStarted)); n != 0 {
		t.Fatalf("tool.call.started events = %d while waiting, want 0", n)
	}
	var released bool
	if err := r.pool.QueryRow(t.Context(), `SELECT lease_owner IS NULL FROM sessions WHERE id = $1`, r.sid).Scan(&released); err != nil || !released {
		t.Fatalf("lease released = %v (err %v), want true", released, err)
	}

	r.stop()
	r.start(t, r.p, r.builtin)
	r.resolve(t, eventlog.Answer{Decision: eventlog.DecisionAllow})
	if card := r.awaitCard(t, 2); card["tool_call_id"] != "c3" {
		t.Fatalf("second card is for %v, want c3", card["tool_call_id"])
	}
	r.resolve(t, eventlog.Answer{Decision: eventlog.DecisionAllow})
	waitStatus(t, r.pool, r.sid, eventlog.StatusAwaitingUser, 2)

	evs = loadEvents(t, r.pool, r.sid)
	var ids, by []any
	for _, e := range ofType(evs, eventlog.TypeToolStarted) {
		ids, by = append(ids, e.Payload["tool_call_id"]), append(by, e.Payload["approved_by"])
	}
	if !slices.Equal(ids, []any{"c1", "c2", "c3"}) || !slices.Equal(by, []any{"mode:ask", "user", "user"}) {
		t.Fatalf("started %v approved by %v, want c1 c2 c3 by mode:ask user user", ids, by)
	}
	if got := r.resultTexts(); !slices.Equal(got, []string{"a ran", "ok x", "ok y"}) {
		t.Fatalf("model saw %v", got)
	}
}

func TestDenyClosesTheCallAndSkipsTheRest(t *testing.T) {
	r := newApprovalRun(t, callsOf("notion__x", "notion__y", "notion__z"), done())
	r.begin(t)

	r.awaitCard(t, 1)
	r.resolve(t, eventlog.Answer{Decision: eventlog.DecisionAllow})
	r.awaitCard(t, 2)
	r.resolve(t, eventlog.Answer{Decision: eventlog.DecisionDeny, Reason: "no"})
	waitStatus(t, r.pool, r.sid, eventlog.StatusAwaitingUser, 2)

	evs := loadEvents(t, r.pool, r.sid)
	if n := len(ofType(evs, eventlog.TypeApprovalRequested)); n != 2 {
		t.Fatalf("approval.requested events = %d, want 2: none for the skipped call", n)
	}
	if ids := callIDs(evs, eventlog.TypeToolStarted); !slices.Equal(ids, []string{"c1"}) {
		t.Fatalf("started %v, want only c1", ids)
	}
	denied := map[string]any{}
	for _, e := range ofType(evs, eventlog.TypeToolCompleted) {
		denied[e.Payload["tool_call_id"].(string)] = e.Payload["denied"]
	}
	if len(denied) != 3 || denied["c2"] != true || denied["c3"] != nil || denied["c1"] != nil {
		t.Fatalf("completed calls and their denied flag = %v, want c2 denied only", denied)
	}
	want := []string{"ok x", "denied by user: no", "skipped because an earlier call was denied"}
	if got := r.resultTexts(); !slices.Equal(got, want) {
		t.Fatalf("model saw %v, want %v", got, want)
	}
	for i, part := range r.p.requests()[1].Messages[2].Parts {
		if part.ToolResult.IsError != (i > 0) {
			t.Fatalf("result %d is_error = %v", i+1, part.ToolResult.IsError)
		}
	}
}

func TestApproveAllResolvesTheWholeBatchAtOnce(t *testing.T) {
	r := newApprovalRun(t, callsOf("notion__x", "notion__y", "notion__z"), done())
	r.begin(t)

	r.awaitCard(t, 1)
	r.resolve(t, eventlog.Answer{Decision: eventlog.DecisionAllow, All: true})
	waitStatus(t, r.pool, r.sid, eventlog.StatusAwaitingUser, 2)

	evs := loadEvents(t, r.pool, r.sid)
	if n := len(ofType(evs, eventlog.TypeApprovalRequested)); n != 1 {
		t.Fatalf("approval.requested events = %d, want 1", n)
	}
	resolved := ofType(evs, eventlog.TypeApprovalResolved)
	if len(resolved) != 1 || resolved[0].Payload["all"] != true || resolved[0].Payload["scope"] != "once" {
		t.Fatalf("approval.resolved events = %v, want one with all and scope once", resolved)
	}
	if ids := callIDs(evs, eventlog.TypeToolStarted); !slices.Equal(ids, []string{"c1", "c2", "c3"}) {
		t.Fatalf("started %v, want c1 c2 c3", ids)
	}
}

func TestChangedToolAsksWithAReasonUntilApproved(t *testing.T) {
	r := newApprovalRun(t, callsOf("notion__x"), callsOf("notion__x"), done())
	r.cacheTools(t, "now also deletes")
	changed := connector.FromRaw(mcpclient.RawTool{Name: "x", Description: "now also deletes", InputSchema: json.RawMessage(`{"type":"object"}`)}).Hash
	r.begin(t)

	card := r.awaitCard(t, 1)
	if card["reason"] != "This tool changed since you approved it." {
		t.Fatalf("first card reason = %v", card["reason"])
	}
	r.resolve(t, eventlog.Answer{Decision: eventlog.DecisionAllow})
	var stored string
	if err := r.pool.QueryRow(t.Context(), `SELECT tool_hash FROM connector_tools WHERE connector_id = $1 AND name = 'x'`, r.connID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != changed {
		t.Fatalf("tool_hash = %s, want the changed tool's %s", stored, changed)
	}

	card = r.awaitCard(t, 2)
	if _, ok := card["reason"]; ok {
		t.Fatalf("second card reason = %v, want none", card["reason"])
	}
}
