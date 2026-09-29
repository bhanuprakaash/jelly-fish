package fake_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/fake"
)

func TestStreamEchoesRepeatedToMinReply(t *testing.T) {
	p := fake.Provider{WordDelay: time.Millisecond, MinReply: 10 * time.Millisecond}
	var deltas int
	resp, err := p.Stream(t.Context(), provider.Request{Messages: []msg.Message{msg.UserText("hello there")}}, func(string) { deltas++ })
	if err != nil {
		t.Fatal(err)
	}
	text := resp.Message.Text()
	if !strings.HasPrefix(text, "echo: hello there echo:") {
		t.Fatalf("text = %q", text)
	}
	words := int64(len(strings.Fields(text)))
	if deltas != int(words) || resp.Usage.OutputTokens != words || resp.Usage.InputTokens != 2 {
		t.Fatalf("deltas=%d usage=%+v words=%d", deltas, resp.Usage, words)
	}
	if resp.StopReason != provider.StopReasonEndTurn {
		t.Fatalf("stop = %q", resp.StopReason)
	}
}

func TestStreamSlowPrefixStretchesReply(t *testing.T) {
	p := fake.Provider{WordDelay: time.Millisecond}
	start := time.Now()
	resp, err := p.Stream(t.Context(), provider.Request{Messages: []msg.Message{msg.UserText("/slow 200ms hi")}}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Fatalf("reply took %v, want ~200ms", time.Since(start))
	}
	if strings.Contains(resp.Message.Text(), "/slow") {
		t.Fatalf("prefix leaked into reply: %q", resp.Message.Text())
	}
}

func TestStreamStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	p := fake.Provider{WordDelay: time.Hour}
	if _, err := p.Stream(ctx, provider.Request{}, func(string) {}); err == nil {
		t.Fatal("want ctx error")
	}
}
