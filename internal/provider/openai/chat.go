package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

// Name is OpenAI's Provider name.
const Name = "openai"

// errorPrefix marks a failed tool result: function_call_output has no error
// flag.
const errorPrefix = "Error: "

const typeWebSearchCall = "web_search_call"

type chat struct {
	client  sdk.Client
	catalog Catalog
}

var _ provider.Provider = chat{}

func (chat) Name() string { return Name }

// Stream implements provider.Provider. On any error the partial reply is
// dropped: a half-streamed reasoning or function_call item can't be
// replayed.
func (c chat) Stream(ctx context.Context, req provider.Request, onDelta func(provider.Delta)) (provider.Response, error) {
	params, err := c.params(req)
	if err != nil {
		return provider.Response{}, err
	}
	var httpResp *http.Response
	stream := c.client.Responses.NewStreaming(ctx, params, option.WithResponseInto(&httpResp))
	defer func() { _ = stream.Close() }()

	var final *responses.Response
	var failed provider.Usage
	callIDs := map[int]string{}
	for stream.Next() {
		ev := stream.Current()
		idx := int(ev.OutputIndex)
		switch ev.Type {
		case "response.output_text.delta":
			onDelta(provider.Delta{Idx: idx, Kind: provider.DeltaText, Text: ev.Delta})
		case "response.reasoning_summary_part.added":
			if ev.SummaryIndex > 0 {
				onDelta(provider.Delta{Idx: idx, Kind: provider.DeltaThinking, Text: summarySep})
			}
		case "response.reasoning_summary_text.delta":
			onDelta(provider.Delta{Idx: idx, Kind: provider.DeltaThinking, Text: ev.Delta})
		case "response.output_item.added":
			if ev.Item.Type == "function_call" {
				callIDs[idx] = uuid.NewString()
				onDelta(provider.Delta{Idx: idx, Kind: provider.DeltaToolStart, CallID: callIDs[idx], Name: ev.Item.Name})
			}
		case "response.function_call_arguments.delta":
			onDelta(provider.Delta{Idx: idx, Kind: provider.DeltaToolArgs, CallID: callIDs[idx], Text: ev.Delta})
		case "response.completed", "response.incomplete":
			final = &ev.Response
		case "response.failed":
			err = codeError(ev.Response.Error.Code)
			failed = usage(ev.Response.Usage)
		case "error":
			err = codeError(ev.Code)
		}
	}
	if err == nil {
		err = stream.Err()
	}
	if err == nil && final == nil {
		err = errors.New("openai stream ended before the response completed")
	}
	if err != nil {
		if errCode(err) == "context_length_exceeded" {
			// A stop, not an error: the loop compacts and retries
			// (provider-gateway.md D10).
			return provider.Response{Message: msg.Message{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{}}, StopReason: provider.StopReasonContextExceeded, RequestID: requestID(httpResp)}, nil
		}
		return provider.Response{}, classify(ctx, err, failed, requestID(httpResp))
	}

	m := fromResponse(*final, req.Model, func(i int) string { return callIDs[i] })
	u := usage(final.Usage)
	// The usage object has no search count. open_page and find_in_page cost
	// input tokens, already recorded; any other call, including one with no
	// action, counts as a search so the estimate errs high.
	for _, p := range m.Parts {
		if p.Kind == msg.KindNative && p.Native.Type == typeWebSearchCall {
			var item struct {
				Action struct {
					Type string `json:"type"`
				} `json:"action"`
			}
			_ = json.Unmarshal(p.Native.Raw, &item)
			if t := item.Action.Type; t != "open_page" && t != "find_in_page" {
				u.WebSearches++
			}
		}
	}
	return provider.Response{
		Message:    m,
		StopReason: stopReason(*final, m),
		Usage:      u,
		RequestID:  requestID(httpResp),
	}, nil
}

func requestID(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	return resp.Header.Get("x-request-id")
}

// summarySep separates reasoning summary parts.
const summarySep = "\n\n"

func (c chat) params(req provider.Request) (responses.ResponseNewParams, error) {
	var info provider.ModelInfo
	known := false
	if c.catalog != nil {
		info, known = c.catalog.Lookup(req.Model)
	}
	input, err := toInput(req.System, req.Messages)
	if err != nil {
		return responses.ResponseNewParams{}, err
	}
	p := responses.ResponseNewParams{
		Model: req.Model,
		Input: responses.ResponseNewParamsInputUnion{OfInputItemList: input},
		// The full prompt goes every turn; OpenAI keeps nothing
		// (provider-gateway.md D5).
		Store: sdk.Bool(false),
	}
	if req.CacheKey != "" {
		p.PromptCacheKey = sdk.String(req.CacheKey)
	}
	if known && info.Thinking == provider.ThinkingAlwaysOn && !req.NoThinking {
		// Summaries to show, encrypted content to replay; no effort is sent
		// (provider-gateway.md D17).
		p.Reasoning = shared.ReasoningParam{Summary: shared.ReasoningSummaryAuto}
		p.Include = []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent}
	}
	for _, t := range req.Tools {
		// Sent as given, so every adapter shares the one schema. strict is
		// always explicit: OpenAI turns it on when it's missing.
		raw, err := json.Marshal(struct {
			Type        string          `json:"type"`
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
			Strict      bool            `json:"strict"`
		}{"function", t.Name, t.Description, t.Schema, t.Strict && known && info.Structured})
		if err != nil {
			return responses.ResponseNewParams{}, fmt.Errorf("openai: tool %s: %w", t.Name, err)
		}
		p.Tools = append(p.Tools, param.Override[responses.ToolUnionParam](json.RawMessage(raw)))
	}
	if req.WebSearch {
		p.Tools = append(p.Tools, responses.ToolParamOfWebSearch(responses.WebSearchToolTypeWebSearch))
	}
	return p, nil
}

// toInput builds the input items: the system prompt as one leading developer
// item, inside the cached prefix, then each part. Another Provider's
// Reasoning and Native parts are dropped (provider-gateway.md §5.1). A call
// OpenAI wrote goes out under its own call id, any other under ours; its
// result must carry the same one.
func toInput(system []string, messages []msg.Message) (responses.ResponseInputParam, error) {
	var out responses.ResponseInputParam
	if len(system) > 0 {
		var content responses.ResponseInputMessageContentListParam
		for _, s := range system {
			content = append(content, responses.ResponseInputContentUnionParam{OfInputText: &responses.ResponseInputTextParam{Text: s}})
		}
		out = append(out, responses.ResponseInputItemParamOfMessage(content, responses.EasyInputMessageRoleDeveloper))
	}
	wireID := map[string]string{}
	for _, m := range messages {
		role := responses.EasyInputMessageRoleUser
		if m.Role == msg.RoleAssistant {
			role = responses.EasyInputMessageRoleAssistant
		}
		parts := m.Parts
		// A reasoning item must be followed by the item it led to; a reply
		// cut off after reasoning (its tool call removed) has none.
		for len(parts) > 0 && parts[len(parts)-1].Kind == msg.KindThinking {
			parts = parts[:len(parts)-1]
		}
		for _, p := range parts {
			item, ok, err := toItem(p, role, wireID)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, item)
			}
		}
	}
	return out, nil
}

func toItem(p msg.Part, role responses.EasyInputMessageRole, wireID map[string]string) (responses.ResponseInputItemUnionParam, bool, error) {
	switch p.Kind {
	case msg.KindText:
		return responses.ResponseInputItemParamOfMessage(p.Text, role), true, nil
	case msg.KindThinking:
		if p.Thinking.Provider != Name {
			return responses.ResponseInputItemUnionParam{}, false, nil
		}
		// Opaque is the whole reasoning item, sent back as it came.
		return param.Override[responses.ResponseInputItemUnionParam](json.RawMessage(p.Thinking.Opaque)), true, nil
	case msg.KindNative:
		if p.Native.Provider != Name {
			return responses.ResponseInputItemUnionParam{}, false, nil
		}
		return param.Override[responses.ResponseInputItemUnionParam](p.Native.Raw), true, nil
	case msg.KindToolUse:
		id := p.ToolUse.ID
		if len(p.ToolUse.Opaque) > 0 && p.ToolUse.Provider == Name {
			id = string(p.ToolUse.Opaque)
		}
		wireID[p.ToolUse.ID] = id
		return responses.ResponseInputItemParamOfFunctionCall(string(p.ToolUse.Args), id, p.ToolUse.Name), true, nil
	case msg.KindToolResult:
		tr := p.ToolResult
		id, ok := wireID[tr.CallID]
		if !ok {
			id = tr.CallID
		}
		var texts []string
		for _, rp := range tr.Parts {
			if rp.Kind != msg.KindText {
				return responses.ResponseInputItemUnionParam{}, false, fmt.Errorf("openai: tool result part %q not supported", rp.Kind)
			}
			texts = append(texts, rp.Text)
		}
		if tr.IsError && len(texts) > 0 {
			texts[0] = errorPrefix + texts[0]
		}
		var item responses.ResponseInputItemUnionParam
		if len(texts) == 1 {
			item = responses.ResponseInputItemParamOfFunctionCallOutput(texts[0])
		} else {
			var list responses.ResponseFunctionCallOutputItemListParam
			for _, s := range texts {
				list = append(list, responses.ResponseFunctionCallOutputItemParamOfInputText(s))
			}
			item = responses.ResponseInputItemParamOfFunctionCallOutput(list)
		}
		item.OfFunctionCallOutput.CallID = sdk.String(id)
		return item, true, nil
	}
	return responses.ResponseInputItemUnionParam{}, false, fmt.Errorf("openai: part kind %q not supported", p.Kind)
}

// fromResponse turns a reply into a neutral Message, one part per output
// item, so a Delta's Idx is the item's output index. OpenAI's own call id is
// kept as Opaque.
func fromResponse(r responses.Response, model string, callID func(int) string) msg.Message {
	out := msg.Message{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: make([]msg.Part, 0, len(r.Output))}
	for i, item := range r.Output {
		var p msg.Part
		switch item.Type {
		case "message":
			// A refusal is shown as the reply text.
			var text strings.Builder
			var cites []msg.Citation
			for _, c := range item.Content {
				text.WriteString(c.Text)
				text.WriteString(c.Refusal)
				for _, a := range c.Annotations {
					if a.Type == "url_citation" {
						cites = append(cites, msg.Citation{URL: a.URL, Title: a.Title})
					}
				}
			}
			p = msg.Part{Kind: msg.KindText, Text: text.String(), Citations: cites}
		case "reasoning":
			var summary []string
			for _, s := range item.AsReasoning().Summary {
				summary = append(summary, s.Text)
			}
			p = msg.Part{Kind: msg.KindThinking, Thinking: &msg.Thinking{Text: strings.Join(summary, summarySep), Provider: Name, Model: model, Opaque: []byte(item.RawJSON())}}
		case "function_call":
			fc := item.AsFunctionCall()
			id := callID(i)
			if id == "" {
				id = uuid.NewString()
			}
			// Empty bytes are not JSON, and Args is stored as JSON.
			args := json.RawMessage(fc.Arguments)
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			p = msg.Part{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: id, Name: fc.Name, Args: args, Opaque: []byte(fc.CallID)}}
		default:
			p = msg.Part{Kind: msg.KindNative, Native: &msg.Native{Provider: Name, Type: item.Type, Raw: json.RawMessage(item.RawJSON())}}
		}
		out.Parts = append(out.Parts, p)
	}
	return out
}

func stopReason(r responses.Response, m msg.Message) provider.StopReason {
	if r.Status == responses.ResponseStatusIncomplete {
		switch r.IncompleteDetails.Reason {
		case "max_output_tokens":
			return provider.StopReasonMaxTokens
		case "content_filter":
			return provider.StopReasonRefusal
		}
		return provider.StopReasonOther
	}
	if r.Status != responses.ResponseStatusCompleted {
		return provider.StopReasonOther
	}
	for _, p := range m.Parts {
		if p.Kind == msg.KindToolUse {
			return provider.StopReasonToolUse
		}
	}
	return provider.StopReasonEndTurn
}

// usage normalizes OpenAI's counts: input_tokens includes cache reads and
// writes, which are taken out (provider-gateway.md §5.3).
func usage(u responses.ResponseUsage) provider.Usage {
	read := u.InputTokensDetails.CachedTokens
	written := u.InputTokensDetails.CacheWriteTokens
	return provider.Usage{
		Input:     u.InputTokens - read - written,
		CacheRead: read,
		// OpenAI has one cache-write rate; the catalog keeps it as 5m.
		CacheWrite5m: written,
		Output:       u.OutputTokens,
		Reasoning:    u.OutputTokensDetails.ReasoningTokens,
	}
}
