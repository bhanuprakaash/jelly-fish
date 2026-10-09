package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

// Name is the Provider label for Anthropic, in events, usage rows and the
// Provider field of the parts it writes.
const Name = "anthropic"

// defaultMaxOutput caps a model the catalog doesn't know
// (provider-gateway.md §5.5 item 4).
const defaultMaxOutput = 8192

// Native block types Anthropic's replay accepts, sent back as stored.
const (
	typeRedactedThinking = "redacted_thinking"
	typeServerToolUse    = "server_tool_use"
	typeWebSearchResult  = "web_search_tool_result"
	typeWebFetchResult   = "web_fetch_tool_result"
)

// Provider returns a Provider that streams replies on key.
func (c Client) Provider(key string) provider.Provider {
	return chat{client: sdk.NewClient(c.options(key)...), catalog: c.Catalog}
}

func (c Client) options(key string) []option.RequestOption {
	// Retries belong to our own error layer, which counts them
	// (agent-loop.md §5.3), never to the SDK.
	opts := []option.RequestOption{option.WithAPIKey(key), option.WithMaxRetries(0)}
	if c.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(c.BaseURL))
	}
	return append(opts, option.WithHTTPClient(provider.WatchedClient(c.HTTPClient, c.IdleTimeout)))
}

type chat struct {
	client  sdk.Client
	catalog Catalog
}

var _ provider.Provider = chat{}

func (chat) Name() string { return Name }

// Stream implements provider.Provider. On any error the partial reply is
// dropped: a half-streamed thinking or tool_use block can't be replayed.
func (c chat) Stream(ctx context.Context, req provider.Request, onDelta func(provider.Delta)) (provider.Response, error) {
	params, err := c.params(req)
	if err != nil {
		return provider.Response{}, err
	}
	var httpResp *http.Response
	stream := c.client.Messages.NewStreaming(ctx, params, option.WithResponseInto(&httpResp))
	defer func() { _ = stream.Close() }()

	var acc sdk.Message
	callIDs := map[int]string{}
	for stream.Next() {
		ev := stream.Current()
		if err := acc.Accumulate(ev); err != nil {
			return provider.Response{}, fmt.Errorf("anthropic stream: %w", err)
		}
		idx := int(ev.Index)
		switch ev.Type {
		case "content_block_start":
			if ev.ContentBlock.Type == "tool_use" {
				callIDs[idx] = uuid.NewString()
				onDelta(provider.Delta{Idx: idx, Kind: provider.DeltaToolStart, CallID: callIDs[idx], Name: ev.ContentBlock.Name})
			}
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				onDelta(provider.Delta{Idx: idx, Kind: provider.DeltaText, Text: ev.Delta.Text})
			case "thinking_delta":
				onDelta(provider.Delta{Idx: idx, Kind: provider.DeltaThinking, Text: ev.Delta.Thinking})
			case "input_json_delta":
				onDelta(provider.Delta{Idx: idx, Kind: provider.DeltaToolArgs, CallID: callIDs[idx], Text: ev.Delta.PartialJSON})
			}
		}
	}
	if err := stream.Err(); err != nil {
		return provider.Response{}, classify(ctx, err, usage(acc.Usage), requestID(httpResp))
	}

	resp := provider.Response{
		Message:    fromMessage(acc, req.Model, func(i int) string { return callIDs[i] }),
		StopReason: stopReason(acc.StopReason),
		StopDetail: stopDetail(acc.StopDetails),
		Usage:      usage(acc.Usage),
	}
	resp.RequestID = requestID(httpResp)
	return resp, nil
}

func requestID(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	return resp.Header.Get("request-id")
}

func (c chat) params(req provider.Request) (sdk.MessageNewParams, error) {
	var info provider.ModelInfo
	known := false
	if c.catalog != nil {
		info, known = c.catalog.Lookup(req.Model)
	}
	maxOut := int64(defaultMaxOutput)
	if known {
		maxOut = info.MaxOutput
	}
	messages, from, err := toParams(req.Messages)
	if err != nil {
		return sdk.MessageNewParams{}, err
	}
	p := sdk.MessageNewParams{Model: sdk.Model(req.Model), MaxTokens: maxOut, Messages: messages}
	for _, s := range req.System {
		p.System = append(p.System, sdk.TextBlockParam{Text: s})
	}
	for _, t := range req.Tools {
		// Sent as given, so every adapter shares the one schema.
		schema := param.Override[sdk.ToolInputSchemaParam](t.Schema)
		tp := &sdk.ToolParam{Name: t.Name, Description: sdk.String(t.Description), InputSchema: schema}
		if t.Strict && known && info.Structured {
			tp.Strict = sdk.Bool(true)
		}
		p.Tools = append(p.Tools, sdk.ToolUnionParam{OfTool: tp})
	}
	if req.WebSearch {
		p.Tools = append(p.Tools,
			sdk.ToolUnionParam{OfWebSearchTool20250305: &sdk.WebSearchTool20250305Param{}},
			sdk.ToolUnionParam{OfWebFetchTool20250910: &sdk.WebFetchTool20250910Param{}})
	}
	if known && !req.NoThinking && info.Thinking == provider.ThinkingAdaptiveOnly {
		// Newer models default display to "omitted"; summarized is the only
		// change from the default, so the prefix stays byte-stable (D17).
		p.Thinking = sdk.ThinkingConfigParamUnion{OfAdaptive: &sdk.ThinkingConfigAdaptiveParam{Display: sdk.ThinkingConfigAdaptiveDisplaySummarized}}
	}
	setBreakpoints(&p, from, req.SummaryEnd)
	return p, nil
}

// setBreakpoints marks the prompt cache: after the system prompt, after the
// Compaction summary, and a moving one on the last block (context.md §5.2,
// D13). from[i] is the neutral index p.Messages[i] came from. A breakpoint
// goes on the last block that can carry one; thinking blocks can't.
func setBreakpoints(p *sdk.MessageNewParams, from []int, summaryEnd int) {
	if n := len(p.System); n > 0 {
		p.System[n-1].CacheControl = sdk.NewCacheControlEphemeralParam()
	}
	for i := len(p.Messages) - 1; summaryEnd > 0 && i >= 0; i-- {
		if from[i] < summaryEnd {
			markLast(p.Messages[i].Content)
			break
		}
	}
	if n := len(p.Messages); n > 0 {
		markLast(p.Messages[n-1].Content)
	}
}

// markLast sets the breakpoint on the last block that can carry one. Blocks
// built from stored JSON are marshalled as stored, so a setting on them is
// lost.
func markLast(blocks []sdk.ContentBlockParamUnion) {
	for i := len(blocks) - 1; i >= 0; i-- {
		if b := blocks[i]; b.OfServerToolUse != nil || b.OfWebSearchToolResult != nil || b.OfWebFetchToolResult != nil {
			continue
		}
		if cc := blocks[i].GetCacheControl(); cc != nil {
			*cc = sdk.NewCacheControlEphemeralParam()
			return
		}
	}
}

// toParams maps neutral messages onto Anthropic's, and returns the index of
// the neutral message each one came from. Thinking and Native parts from
// another Provider are dropped (provider-gateway.md §5.1), and a message left
// empty with them. A tool call Anthropic wrote goes out under its own id,
// any other under ours; its result follows it.
func toParams(messages []msg.Message) ([]sdk.MessageParam, []int, error) {
	wireID := map[string]string{}
	out := make([]sdk.MessageParam, 0, len(messages))
	from := make([]int, 0, len(messages))
	for mi, m := range messages {
		var blocks []sdk.ContentBlockParamUnion
		parts := m.Parts
		if mi < len(messages)-1 {
			parts = withoutUnansweredServerToolUse(parts)
		}
		for _, p := range parts {
			b, ok, err := toBlock(p, wireID)
			if err != nil {
				return nil, nil, err
			}
			if ok {
				blocks = append(blocks, b)
			}
		}
		if len(blocks) == 0 {
			continue
		}
		role := sdk.MessageParamRoleUser
		if m.Role == msg.RoleAssistant {
			role = sdk.MessageParamRoleAssistant
		}
		out = append(out, sdk.MessageParam{Role: role, Content: blocks})
		from = append(from, mi)
	}
	return out, from, nil
}

// withoutUnansweredServerToolUse drops a server_tool_use block with no result
// block in the same message. A reply the pause cap cut off ends in one, and
// Anthropic rejects it anywhere but last, where a resume sends it as is.
func withoutUnansweredServerToolUse(parts []msg.Part) []msg.Part {
	answered := map[string]bool{}
	for _, p := range parts {
		if p.Kind == msg.KindNative && p.Native.Provider == Name && (p.Native.Type == typeWebSearchResult || p.Native.Type == typeWebFetchResult) {
			answered[nativeIDs(p.Native.Raw).ToolUseID] = true
		}
	}
	return slices.DeleteFunc(slices.Clone(parts), func(p msg.Part) bool {
		return p.Kind == msg.KindNative && p.Native.Provider == Name && p.Native.Type == typeServerToolUse && !answered[nativeIDs(p.Native.Raw).ID]
	})
}

type blockIDs struct {
	ID        string `json:"id"`
	ToolUseID string `json:"tool_use_id"`
}

func nativeIDs(raw json.RawMessage) blockIDs {
	var ids blockIDs
	_ = json.Unmarshal(raw, &ids)
	return ids
}

func toBlock(p msg.Part, wireID map[string]string) (sdk.ContentBlockParamUnion, bool, error) {
	switch p.Kind {
	case msg.KindText:
		return sdk.NewTextBlock(p.Text), true, nil
	case msg.KindThinking:
		if p.Thinking.Provider != Name {
			return sdk.ContentBlockParamUnion{}, false, nil
		}
		return sdk.NewThinkingBlock(string(p.Thinking.Opaque), p.Thinking.Text), true, nil
	case msg.KindNative:
		if p.Native.Provider != Name {
			return sdk.ContentBlockParamUnion{}, false, nil
		}
		// Sent as stored, so the block reaches Anthropic byte for byte.
		switch p.Native.Type {
		case typeRedactedThinking:
			b := param.Override[sdk.RedactedThinkingBlockParam](p.Native.Raw)
			return sdk.ContentBlockParamUnion{OfRedactedThinking: &b}, true, nil
		case typeServerToolUse:
			b := param.Override[sdk.ServerToolUseBlockParam](p.Native.Raw)
			return sdk.ContentBlockParamUnion{OfServerToolUse: &b}, true, nil
		case typeWebSearchResult:
			b := param.Override[sdk.WebSearchToolResultBlockParam](p.Native.Raw)
			return sdk.ContentBlockParamUnion{OfWebSearchToolResult: &b}, true, nil
		case typeWebFetchResult:
			b := param.Override[sdk.WebFetchToolResultBlockParam](p.Native.Raw)
			return sdk.ContentBlockParamUnion{OfWebFetchToolResult: &b}, true, nil
		}
		return sdk.ContentBlockParamUnion{}, false, fmt.Errorf("anthropic: can't replay native %q", p.Native.Type)
	case msg.KindToolUse:
		id := p.ToolUse.ID
		if len(p.ToolUse.Opaque) > 0 && p.ToolUse.Provider == Name {
			id = string(p.ToolUse.Opaque)
		}
		wireID[p.ToolUse.ID] = id
		return sdk.NewToolUseBlock(id, json.RawMessage(p.ToolUse.Args), p.ToolUse.Name), true, nil
	case msg.KindToolResult:
		tr := p.ToolResult
		id, ok := wireID[tr.CallID]
		if !ok {
			id = tr.CallID
		}
		b := sdk.ToolResultBlockParam{ToolUseID: id}
		if tr.IsError {
			b.IsError = sdk.Bool(true)
		}
		for _, rp := range tr.Parts {
			if rp.Kind != msg.KindText {
				return sdk.ContentBlockParamUnion{}, false, fmt.Errorf("anthropic: tool result part %q not supported", rp.Kind)
			}
			b.Content = append(b.Content, sdk.ToolResultBlockParamContentUnion{OfText: &sdk.TextBlockParam{Text: rp.Text}})
		}
		return sdk.ContentBlockParamUnion{OfToolResult: &b}, true, nil
	}
	return sdk.ContentBlockParamUnion{}, false, fmt.Errorf("anthropic: part kind %q not supported", p.Kind)
}

// fromMessage turns a reply into a neutral Message. callID gives our id for
// the tool_use block at an index; Anthropic's own id is kept as Opaque.
// Blocks with no neutral kind are kept whole as Native.
func fromMessage(m sdk.Message, model string, callID func(int) string) msg.Message {
	out := msg.Message{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: make([]msg.Part, 0, len(m.Content))}
	for i, b := range m.Content {
		var p msg.Part
		switch b.Type {
		case "text":
			p = msg.Part{Kind: msg.KindText, Text: b.Text, Citations: citations(b.Citations)}
		case "thinking":
			p = msg.Part{Kind: msg.KindThinking, Thinking: &msg.Thinking{Text: b.Thinking, Provider: Name, Model: model, Opaque: []byte(b.Signature)}}
		case "tool_use":
			id := callID(i)
			if id == "" {
				id = uuid.NewString()
			}
			p = msg.Part{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: id, Name: b.Name, Args: b.Input, Opaque: []byte(b.ID)}}
		default:
			p = msg.Part{Kind: msg.KindNative, Native: &msg.Native{Provider: Name, Type: b.Type, Raw: json.RawMessage(b.RawJSON())}}
		}
		out.Parts = append(out.Parts, p)
	}
	return out
}

// citations keeps the web pages a text block cites; other citation kinds
// point into documents we sent.
func citations(cs []sdk.TextCitationUnion) []msg.Citation {
	var out []msg.Citation
	for _, c := range cs {
		if c.Type == "web_search_result_location" {
			out = append(out, msg.Citation{URL: c.URL, Title: c.Title})
		}
	}
	return out
}

func stopReason(r sdk.StopReason) provider.StopReason {
	switch r {
	case sdk.StopReasonEndTurn:
		return provider.StopReasonEndTurn
	case sdk.StopReasonMaxTokens:
		return provider.StopReasonMaxTokens
	case sdk.StopReasonStopSequence:
		return provider.StopReasonStopSequence
	case sdk.StopReasonToolUse:
		return provider.StopReasonToolUse
	case sdk.StopReasonPauseTurn:
		return provider.StopReasonPauseTurn
	case sdk.StopReasonRefusal:
		return provider.StopReasonRefusal
	case sdk.StopReasonModelContextWindowExceeded:
		return provider.StopReasonContextExceeded
	}
	return provider.StopReasonOther
}

// stopDetail joins a refusal's category and explanation; either may be null.
func stopDetail(d sdk.RefusalStopDetails) string {
	parts := []string{string(d.Category), d.Explanation}
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), ". ")
}

// usage normalizes Anthropic's counts; input_tokens already excludes cache
// reads and writes (provider-gateway.md §5.3).
func usage(u sdk.Usage) provider.Usage {
	out := provider.Usage{
		Input:        u.InputTokens,
		CacheRead:    u.CacheReadInputTokens,
		CacheWrite5m: u.CacheCreation.Ephemeral5mInputTokens,
		CacheWrite1h: u.CacheCreation.Ephemeral1hInputTokens,
		Output:       u.OutputTokens,
		Reasoning:    u.OutputTokensDetails.ThinkingTokens,
		WebSearches:  u.ServerToolUse.WebSearchRequests,
	}
	if out.CacheWrite5m+out.CacheWrite1h == 0 {
		// No per-TTL split reported: every breakpoint we set is 5m.
		out.CacheWrite5m = u.CacheCreationInputTokens
	}
	return out
}
