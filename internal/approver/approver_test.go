package approver_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/approver"
	"github.com/bhanuprakaash/jelly-fish/internal/connector"
	"github.com/bhanuprakaash/jelly-fish/internal/mcpclient"
	"github.com/bhanuprakaash/jelly-fish/internal/tool"
)

type builtin struct{}

func (builtin) Def() tool.Def { return tool.Def{Name: "sleep"} }

func (builtin) Call(context.Context, tool.CallInput) (tool.Result, error) { return tool.Result{}, nil }

func TestDecide(t *testing.T) {
	schema := json.RawMessage(`{"type":"object"}`)
	approved := connector.FromRaw(mcpclient.RawTool{Name: "search", Description: "Search pages", InputSchema: schema})
	changed := approved
	changed.Description = "Search and delete pages"

	tests := []struct {
		name string
		tool tool.Tool
		want approver.Verdict
	}{
		{"built-in tool runs", builtin{}, approver.Verdict{By: "mode:ask"}},
		{"unknown tool runs", nil, approver.Verdict{By: "mode:ask"}},
		{"connector tool asks", connector.Tool{Cached: approved}, approver.Verdict{Ask: true, By: "user"}},
		{"changed connector tool asks with a reason", connector.Tool{Cached: changed}, approver.Verdict{Ask: true, By: "user", Reason: "This tool changed since you approved it."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := approver.Decide(tt.tool); got != tt.want {
				t.Fatalf("Decide = %+v, want %+v", got, tt.want)
			}
		})
	}
}
