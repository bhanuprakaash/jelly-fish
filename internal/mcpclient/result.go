package mcpclient

import (
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
)

// mapResult turns an SDK result into a Result per docs/design/mcp-client.md
// §5.2. msg has no image Kind, so an image rides as a native part until the
// blob store can hold it.
func mapResult(res *mcp.CallToolResult) (Result, error) {
	if res.NeedsInput() {
		ir := &InputRequired{Requests: map[string]InputRequest{}, RequestState: res.RequestState}
		for key, req := range res.InputRequests {
			p, ok := req.(*mcp.ElicitParams)
			if !ok {
				return Result{}, fmt.Errorf("input request %q: unsupported type %T", key, req)
			}
			r, err := inputRequest(p)
			if err != nil {
				return Result{}, fmt.Errorf("input request %q: %w", key, err)
			}
			ir.Requests[key] = r
		}
		return Result{InputRequired: ir}, nil
	}
	out := Result{IsError: res.IsError}
	for _, c := range res.Content {
		switch c := c.(type) {
		case *mcp.TextContent:
			out.Content = append(out.Content, text(c.Text))
		case *mcp.ImageContent:
			raw, err := json.Marshal(map[string]any{"mimeType": c.MIMEType, "data": c.Data})
			if err != nil {
				return Result{}, fmt.Errorf("encode image: %w", err)
			}
			out.Content = append(out.Content, msg.Part{Kind: msg.KindNative, Native: &msg.Native{Provider: "mcp", Type: "image", Raw: raw}})
		case *mcp.ResourceLink:
			out.Content = append(out.Content, text(c.URI))
		case *mcp.AudioContent:
			out.Content = append(out.Content, text("[audio returned, not supported]"))
		default:
			out.Content = append(out.Content, text("[content returned, not supported]"))
		}
	}
	if len(out.Content) == 0 && res.StructuredContent != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			return Result{}, fmt.Errorf("encode structured content: %w", err)
		}
		out.Content = append(out.Content, text(string(b)))
	}
	return out, nil
}

func inputRequest(p *mcp.ElicitParams) (InputRequest, error) {
	var schema json.RawMessage
	if p.RequestedSchema != nil {
		var err error
		if schema, err = json.Marshal(p.RequestedSchema); err != nil {
			return InputRequest{}, fmt.Errorf("encode schema: %w", err)
		}
	}
	mode := p.Mode
	if mode == "" {
		mode = "form"
		if p.URL != "" {
			mode = "url"
		}
	}
	return InputRequest{Mode: mode, Message: p.Message, RequestedSchema: schema, URL: p.URL}, nil
}

func text(s string) msg.Part { return msg.Part{Kind: msg.KindText, Text: s} }

func sdkResponse(r InputResponse) (*mcp.ElicitResult, error) {
	res := &mcp.ElicitResult{Action: r.Action}
	if len(r.Content) > 0 {
		if err := json.Unmarshal(r.Content, &res.Content); err != nil {
			return nil, fmt.Errorf("decode answer: %w", err)
		}
	}
	return res, nil
}

func sdkResponses(in map[string]InputResponse) (mcp.InputResponseMap, error) {
	out := make(mcp.InputResponseMap, len(in))
	for key, r := range in {
		res, err := sdkResponse(r)
		if err != nil {
			return nil, fmt.Errorf("input response %q: %w", key, err)
		}
		out[key] = res
	}
	return out, nil
}
