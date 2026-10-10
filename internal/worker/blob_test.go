package worker_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/bhanuprakaash/jelly-fish/internal/blob"
	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
	"github.com/bhanuprakaash/jelly-fish/internal/tool"
)

func returns(id, text string) fn {
	return fn{id: id, call: func(context.Context) tool.Result { return tool.TextResult(text, false) }}
}

func blobRef(t *testing.T, payload map[string]any, key string) blob.Ref {
	t.Helper()
	raw, err := json.Marshal(payload[key])
	if err != nil {
		t.Fatal(err)
	}
	var ref blob.Ref
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestLargeToolResultIsABlobAndSmallStaysInline(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newFakeSession(t, pool)
	large, small := strings.Repeat("x", 100<<10), strings.Repeat("y", 20<<10)
	p := &replies{list: []provider.Response{toolUse("a", "b"), done()}}
	startTools(t, pool, p, 10*time.Second, tool.NewRegistry(returns("a", large), returns("b", small)))
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 2)

	byID := map[string]map[string]any{}
	for _, e := range ofType(loadEvents(t, pool, sid), eventlog.TypeToolCompleted) {
		byID[e.Payload["tool_call_id"].(string)] = e.Payload
	}
	if _, ok := byID["b"]["blob_ref"]; ok {
		t.Fatalf("20 KB result is not inline: %v", byID["b"])
	}
	a := byID["a"]
	if _, ok := a["result"]; ok {
		t.Fatal("100 KB result kept inline")
	}
	if got := a["preview"].(string); got != large[:2048] {
		t.Fatalf("preview is %d bytes, want the first 2048", len(got))
	}
	ref := blobRef(t, a, "blob_ref")
	mime, data, err := blob.Get(t.Context(), pool, ref.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if mime != "application/json" || ref.Mime != mime || ref.Size != len(data) || ref.SHA256 != hex.EncodeToString(sum[:]) || ref.Key != ref.SHA256 {
		t.Fatalf("ref = %+v for %d bytes of %s", ref, len(data), mime)
	}
	var content []msg.Part
	if err := json.Unmarshal(data, &content); err != nil || len(content) != 1 || content[0].Text != large {
		t.Fatalf("stored content is not the 102400-byte result: %v", err)
	}

	// The turn's tool list is the other blob.
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM blobs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("blobs = %d, want 2: the tool list and the large result", n)
	}

	results := p.requests()[1].Messages[2].Parts
	if got, want := results[0].ToolResult.Parts[0].Text, fmt.Sprintf("%s\n[result truncated: %d bytes, see blob]", large[:2048], ref.Size); got != want {
		t.Fatalf("model saw %d bytes ending %q", len(got), got[len(got)-50:])
	}
	if got := results[1].ToolResult.Parts[0].Text; got != small {
		t.Fatal("small result changed")
	}
	assertFoldMatchesRow(t, pool, sid)
}

func TestSameLargeResultTwiceIsOneBlob(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newFakeSession(t, pool)
	large := strings.Repeat("x", 100<<10)
	p := &replies{list: []provider.Response{toolUse("a", "b"), done()}}
	startTools(t, pool, p, 10*time.Second, tool.NewRegistry(returns("a", large), returns("b", large)))
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 2)

	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM blobs WHERE size > 100000`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("large blobs = %d, want 1", n)
	}
}

func TestTurnStartedStoresTheToolList(t *testing.T) {
	pool := testdb.NewPool(t)
	sid, _ := newFakeSession(t, pool)
	startTools(t, pool, &replies{list: []provider.Response{done()}}, 10*time.Second, tool.NewRegistry(returns("a", "ok"), returns("b", "ok")))
	waitStatus(t, pool, sid, eventlog.StatusAwaitingUser, 1)

	started := ofType(loadEvents(t, pool, sid), eventlog.TypeTurnStarted)[0].Payload
	ref := blobRef(t, started, "tools_blob")
	_, data, err := blob.Get(t.Context(), pool, ref.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	var defs []tool.Def
	if err := json.Unmarshal(data, &defs); err != nil {
		t.Fatal(err)
	}
	hash, err := tool.Hash(defs)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 2 || defs[0].Name != "tool-a" || defs[1].Name != "tool-b" || hash != started["tools_hash"] {
		t.Fatalf("defs = %+v hashing to %s, want tool-a and tool-b hashing to %v", defs, hash, started["tools_hash"])
	}
}
