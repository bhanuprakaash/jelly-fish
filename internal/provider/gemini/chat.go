package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/genai"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

// Name is Gemini's Provider name.
const Name = "gemini"

// typeGrounding is the Native part holding a search's grounding metadata.
const typeGrounding = "grounding_metadata"

type chat struct {
	client Client
	key    string
	logger *slog.Logger
}

var _ provider.Provider = chat{}

func (chat) Name() string { return Name }

// Stream implements provider.Provider. On any error the partial reply is
// dropped.
func (c chat) Stream(ctx context.Context, req provider.Request, onDelta func(provider.Delta)) (provider.Response, error) {
	tap := &errorTap{}
	client, err := c.client.sdk(ctx, c.key, tap)
	if err != nil {
		return provider.Response{}, fmt.Errorf("gemini client: %w", err)
	}
	contents, err := toContents(req.Messages)
	if err != nil {
		return provider.Response{}, err
	}

	r := reply{model: req.Model, onDelta: onDelta, parts: []msg.Part{}}
	var (
		finish      genai.FinishReason
		finishMsg   string
		block       genai.BlockedReason
		usageMeta   *genai.GenerateContentResponseUsageMetadata
		grounding   *genai.GroundingMetadata
		responseID  string
		streamError error
	)
	for chunk, err := range client.Models.GenerateContentStream(ctx, req.Model, contents, c.config(req)) {
		if err != nil {
			streamError = err
			break
		}
		if tap.err != nil {
			streamError = tap.err
			break
		}
		if chunk.ResponseID != "" {
			responseID = chunk.ResponseID
		}
		if chunk.UsageMetadata != nil {
			usageMeta = chunk.UsageMetadata
		}
		if pf := chunk.PromptFeedback; pf != nil && pf.BlockReason != "" && pf.BlockReason != genai.BlockedReasonUnspecified {
			block = pf.BlockReason
		}
		if len(chunk.Candidates) == 0 {
			continue
		}
		cand := chunk.Candidates[0]
		if cand.Content != nil {
			for _, p := range cand.Content.Parts {
				r.add(p)
			}
		}
		if cand.GroundingMetadata != nil {
			grounding = cand.GroundingMetadata
		}
		if cand.FinishReason != "" && cand.FinishReason != genai.FinishReasonUnspecified {
			finish, finishMsg = cand.FinishReason, tap.finishMsg
		}
	}
	if streamError == nil && finish == "" && block == "" {
		streamError = errors.New("gemini stream ended before the response finished")
	}
	if streamError != nil {
		var apiErr genai.APIError
		if errors.As(streamError, &apiErr) && tooLong(apiErr) {
			// A stop, not an error: the loop compacts and retries
			// (provider-gateway.md D10).
			return provider.Response{Message: msg.Message{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: []msg.Part{}}, StopReason: provider.StopReasonContextExceeded, RequestID: responseID}, nil
		}
		return provider.Response{}, classify(ctx, streamError, usage(usageMeta), responseID)
	}

	u := usage(usageMeta)
	u.WebSearches = r.ground(grounding)
	m := msg.Message{MsgV: msg.CurrentVersion, Role: msg.RoleAssistant, Parts: r.parts}
	stop := stopReason(finish, block, m)
	if stop == provider.StopReasonRefusal || stop == provider.StopReasonOther {
		c.logger.Warn("gemini stopped the reply", "finish_reason", finish, "finish_message", finishMsg, "block_reason", block, "request_id", responseID)
	}
	return provider.Response{Message: m, StopReason: stop, StopDetail: stopDetail(stop, finish, finishMsg, block), Usage: u, RequestID: responseID}, nil
}

func (c chat) config(req provider.Request) *genai.GenerateContentConfig {
	var info provider.ModelInfo
	known := false
	if c.client.Catalog != nil {
		info, known = c.client.Catalog.Lookup(req.Model)
	}
	cfg := &genai.GenerateContentConfig{}
	if len(req.System) > 0 {
		cfg.SystemInstruction = &genai.Content{}
		for _, s := range req.System {
			cfg.SystemInstruction.Parts = append(cfg.SystemInstruction.Parts, &genai.Part{Text: s})
		}
	}
	if known && info.Thinking == provider.ThinkingAlwaysOn && !req.NoThinking {
		// Summaries to show, signatures to replay; no level or budget is
		// sent (provider-gateway.md D17).
		cfg.ThinkingConfig = &genai.ThinkingConfig{IncludeThoughts: true}
	}
	if len(req.Tools) > 0 {
		tool := &genai.Tool{}
		for _, t := range req.Tools {
			// The schema goes as given, so every adapter shares the one;
			// Gemini has no strict mode (provider-gateway.md §4.5).
			tool.FunctionDeclarations = append(tool.FunctionDeclarations, &genai.FunctionDeclaration{Name: t.Name, Description: t.Description, ParametersJsonSchema: t.Schema})
		}
		cfg.Tools = []*genai.Tool{tool}
	}
	if req.WebSearch {
		cfg.Tools = append(cfg.Tools, &genai.Tool{GoogleSearch: &genai.GoogleSearch{}}, &genai.Tool{URLContext: &genai.URLContext{}})
	}
	return cfg
}

// foreignCallSignature is the value Google documents for a function call
// Gemini did not write. Google's JSON carries it as the base64 text of the
// signature bytes, so the SDK's []byte field holds what that text decodes to.
// The SDK re-encodes those bytes as standard base64, so the wire text is
// "skip/thought/signature/validator"; Google's JSON parser reads it as the
// same bytes as the documented literal (verified live).
const foreignCallSignature = "skip_thought_signature_validator"

// toContents builds one Content per message. A Gemini Thinking part holds a
// thought signature, which goes back on the part after it; the summary text
// and another Provider's Thinking and Native parts are dropped
// (provider-gateway.md §5.1). A call Gemini wrote goes out under the id it
// gave, if any; any other goes out under our id and the foreign signature.
// Either way its result carries the same id.
func toContents(messages []msg.Message) ([]*genai.Content, error) {
	type call struct{ name, id string }
	calls := map[string]call{}
	var out []*genai.Content
	for _, m := range messages {
		role := genai.RoleUser
		if m.Role == msg.RoleAssistant {
			role = genai.RoleModel
		}
		var parts []*genai.Part
		var sig []byte
		for _, p := range m.Parts {
			var gp *genai.Part
			switch p.Kind {
			case msg.KindThinking:
				if p.Thinking.Provider == Name && len(p.Thinking.Opaque) > 0 {
					sig = p.Thinking.Opaque
				}
				continue
			case msg.KindNative:
				continue
			case msg.KindText:
				gp = &genai.Part{Text: p.Text}
			case msg.KindToolUse:
				tu := p.ToolUse
				var args map[string]any
				if err := json.Unmarshal(tu.Args, &args); err != nil {
					return nil, fmt.Errorf("gemini: tool call %s args: %w", tu.Name, err)
				}
				id := string(tu.Opaque)
				if tu.Provider != Name {
					id = tu.ID
					sig, _ = base64.URLEncoding.DecodeString(foreignCallSignature)
				}
				calls[tu.ID] = call{name: tu.Name, id: id}
				gp = &genai.Part{FunctionCall: &genai.FunctionCall{ID: id, Name: tu.Name, Args: args}}
			case msg.KindToolResult:
				tr := p.ToolResult
				var texts []string
				for _, rp := range tr.Parts {
					if rp.Kind != msg.KindText {
						return nil, fmt.Errorf("gemini: tool result part %q not supported", rp.Kind)
					}
					texts = append(texts, rp.Text)
				}
				key := "output"
				if tr.IsError {
					key = "error"
				}
				c, ok := calls[tr.CallID]
				if !ok {
					return nil, fmt.Errorf("gemini: tool result for unknown call %s", tr.CallID)
				}
				gp = &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: c.id, Name: c.name, Response: map[string]any{key: strings.Join(texts, "\n")}}}
			default:
				return nil, fmt.Errorf("gemini: part kind %q not supported", p.Kind)
			}
			gp.ThoughtSignature, sig = sig, nil
			parts = append(parts, gp)
		}
		if len(parts) > 0 {
			out = append(out, &genai.Content{Role: role, Parts: parts})
		}
	}
	return out, nil
}

// reply accumulates streamed Gemini parts into neutral ones, so a Delta's
// Idx is the part's index in the final Message. Gemini splits text across
// chunks; adjacent text, or adjacent thought summary, is one part. A thought
// signature becomes its own Thinking part just before the part it came on.
type reply struct {
	model   string
	onDelta func(provider.Delta)
	parts   []msg.Part
}

func (r *reply) add(p *genai.Part) {
	if len(p.ThoughtSignature) > 0 {
		r.parts = append(r.parts, msg.Part{Kind: msg.KindThinking, Thinking: &msg.Thinking{Provider: Name, Model: r.model, Opaque: p.ThoughtSignature}})
	}
	switch {
	case p.FunctionCall != nil:
		fc := p.FunctionCall
		// Empty bytes are not JSON, and Args is stored as JSON.
		args := json.RawMessage(`{}`)
		if len(fc.Args) > 0 {
			args, _ = json.Marshal(fc.Args)
		}
		id := uuid.NewString()
		idx := len(r.parts)
		r.parts = append(r.parts, msg.Part{Kind: msg.KindToolUse, ToolUse: &msg.ToolUse{ID: id, Name: fc.Name, Args: args, Opaque: []byte(fc.ID)}})
		// Gemini sends the whole call at once, so the one args delta is
		// all of them.
		r.onDelta(provider.Delta{Idx: idx, Kind: provider.DeltaToolStart, CallID: id, Name: fc.Name})
		r.onDelta(provider.Delta{Idx: idx, Kind: provider.DeltaToolArgs, CallID: id, Text: string(args)})
	case p.Text == "":
	case p.Thought:
		idx := len(r.parts) - 1
		if idx < 0 || r.parts[idx].Kind != msg.KindThinking || r.parts[idx].Thinking.Opaque != nil {
			idx++
			r.parts = append(r.parts, msg.Part{Kind: msg.KindThinking, Thinking: &msg.Thinking{Provider: Name, Model: r.model}})
		}
		r.parts[idx].Thinking.Text += p.Text
		r.onDelta(provider.Delta{Idx: idx, Kind: provider.DeltaThinking, Text: p.Text})
	default:
		idx := len(r.parts) - 1
		if idx < 0 || r.parts[idx].Kind != msg.KindText {
			idx++
			r.parts = append(r.parts, msg.Part{Kind: msg.KindText})
		}
		r.parts[idx].Text += p.Text
		r.onDelta(provider.Delta{Idx: idx, Kind: provider.DeltaText, Text: p.Text})
	}
}

// ground keeps a search's grounding metadata as a Native part, so a later
// Provider can tell the reply was grounded, and cites its web sources on the
// last text part. It returns how many searches ran.
func (r *reply) ground(g *genai.GroundingMetadata) int64 {
	if g == nil {
		return 0
	}
	raw, _ := json.Marshal(g)
	r.parts = append(r.parts, msg.Part{Kind: msg.KindNative, Native: &msg.Native{Provider: Name, Type: typeGrounding, Raw: raw}})
	for i := len(r.parts) - 1; i >= 0; i-- {
		if r.parts[i].Kind != msg.KindText {
			continue
		}
		for _, c := range g.GroundingChunks {
			if c.Web != nil {
				r.parts[i].Citations = append(r.parts[i].Citations, msg.Citation{URL: c.Web.URI, Title: c.Web.Title})
			}
		}
		break
	}
	return int64(len(g.WebSearchQueries))
}

// stopReason maps Gemini's finish reason, or the block on the prompt, onto
// ours. Gemini says STOP even when the reply ends in a function call.
func stopReason(finish genai.FinishReason, block genai.BlockedReason, m msg.Message) provider.StopReason {
	if block != "" {
		return provider.StopReasonRefusal
	}
	switch finish {
	case genai.FinishReasonMaxTokens:
		return provider.StopReasonMaxTokens
	case genai.FinishReasonSafety, genai.FinishReasonProhibitedContent, genai.FinishReasonBlocklist,
		genai.FinishReasonSPII, genai.FinishReasonRecitation, genai.FinishReasonImageSafety:
		return provider.StopReasonRefusal
	case genai.FinishReasonStop:
		for _, p := range m.Parts {
			if p.Kind == msg.KindToolUse {
				return provider.StopReasonToolUse
			}
		}
		return provider.StopReasonEndTurn
	}
	return provider.StopReasonOther
}

// stopDetail is the reason and message Gemini gave for a refusal.
func stopDetail(stop provider.StopReason, finish genai.FinishReason, finishMsg string, block genai.BlockedReason) string {
	if stop != provider.StopReasonRefusal {
		return ""
	}
	if block != "" {
		return strings.ToLower(string(block))
	}
	parts := []string{strings.ToLower(string(finish)), finishMsg}
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), ". ")
}

// usage normalizes Gemini's counts: promptTokenCount includes the cached
// tokens, which are taken out. Tool results fed back to the model
// (toolUsePromptTokenCount) are billed as input. Thinking tokens are counted
// apart from candidatesTokenCount and billed as output, so Output holds both
// (provider-gateway.md §5.3).
func usage(u *genai.GenerateContentResponseUsageMetadata) provider.Usage {
	if u == nil {
		return provider.Usage{}
	}
	return provider.Usage{
		Input:     int64(u.PromptTokenCount - u.CachedContentTokenCount + u.ToolUsePromptTokenCount),
		CacheRead: int64(u.CachedContentTokenCount),
		Output:    int64(u.CandidatesTokenCount + u.ThoughtsTokenCount),
		Reasoning: int64(u.ThoughtsTokenCount),
	}
}
