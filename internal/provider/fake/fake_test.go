package fake_test

import (
	"context"
	"slices"
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
	resp, err := p.Stream(t.Context(), provider.Request{Messages: []msg.Message{msg.UserText("hello there")}}, func(provider.Delta) { deltas++ })
	if err != nil {
		t.Fatal(err)
	}
	text := resp.Message.Text()
	if !strings.HasPrefix(text, "echo: hello there echo:") {
		t.Fatalf("text = %q", text)
	}
	words := int64(len(strings.Fields(text)))
	if deltas != int(words) || resp.Usage.Output != words || resp.Usage.Input != 2 {
		t.Fatalf("deltas=%d usage=%+v words=%d", deltas, resp.Usage, words)
	}
	if resp.StopReason != provider.StopReasonEndTurn {
		t.Fatalf("stop = %q", resp.StopReason)
	}
}

func TestStreamSlowPrefixStretchesReply(t *testing.T) {
	p := fake.Provider{WordDelay: time.Millisecond}
	start := time.Now()
	resp, err := p.Stream(t.Context(), provider.Request{Messages: []msg.Message{msg.UserText("/slow 200ms hi")}}, func(provider.Delta) {})
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
	if _, err := p.Stream(ctx, provider.Request{}, func(provider.Delta) {}); err == nil {
		t.Fatal("want ctx error")
	}
}

func TestStreamTitleRequestEchoesCutMessageInstantly(t *testing.T) {
	p := fake.Provider{WordDelay: time.Hour, MinReply: time.Hour}
	long := "plan   a trip to the mountains with a very long description that keeps going on and on and on"
	start := time.Now()
	resp, err := p.Stream(t.Context(), provider.Request{Messages: []msg.Message{msg.UserText(provider.TitlePrompt(long))}}, func(provider.Delta) {
		t.Error("title reply streamed a delta")
	})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatalf("title took %v", time.Since(start))
	}
	want := "plan a trip to the mountains with a very long description th"
	if got := resp.Message.Text(); got != want || len([]rune(got)) != 60 {
		t.Fatalf("title = %q (%d runes), want %q", got, len([]rune(got)), want)
	}
	if resp.StopReason != provider.StopReasonEndTurn || resp.Usage.Input == 0 || resp.Usage.Output != 12 {
		t.Fatalf("stop=%q usage=%+v", resp.StopReason, resp.Usage)
	}
}

func TestStreamTitleRequestDropsTheSlowPrefix(t *testing.T) {
	p := fake.Provider{WordDelay: time.Hour}
	resp, err := p.Stream(t.Context(), provider.Request{Messages: []msg.Message{msg.UserText(provider.TitlePrompt("/slow 30s hello there"))}}, func(provider.Delta) {})
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Message.Text(); got != "hello there" {
		t.Fatalf("title = %q", got)
	}
}

func TestToolCallSendsAJSONObjectInputAsTheArgs(t *testing.T) {
	p := fake.Provider{}
	resp, err := p.Stream(t.Context(), provider.Request{Messages: []msg.Message{
		msg.UserText(`/tool memory {"command":"view","path":"/memories"} ; sleep 2s`),
	}}, func(provider.Delta) {})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, part := range resp.Message.Parts {
		got = append(got, part.ToolUse.Name+" "+string(part.ToolUse.Args))
	}
	want := []string{`memory {"command":"view","path":"/memories"}`, `sleep {"input":"2s"}`}
	if !slices.Equal(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}
