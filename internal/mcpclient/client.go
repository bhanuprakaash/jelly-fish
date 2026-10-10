// Package mcpclient calls tools on remote MCP servers over Streamable HTTP,
// speaking both the 2026-07-28 stateless era and the legacy initialize era
// (docs/design/mcp-client.md).
package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
)

// Era is the MCP protocol era a server speaks.
type Era string

// Eras a Target can have. The zero Era means unknown: the client probes.
const (
	EraModern Era = "modern"
	EraLegacy Era = "legacy"
)

const (
	modernVersion = "2026-07-28"
	legacyVersion = "2025-11-25"
)

// ErrOutdatedServer is returned when a server answers neither the modern nor
// the legacy probe, such as one that speaks only the 2024 HTTP+SSE transport.
var ErrOutdatedServer = errors.New("this server uses an outdated MCP version and can't be added")

// Target is one Connector's endpoint.
type Target struct {
	URL string
	Era Era
	// Header carries the Connector's static credential header, if any. It is
	// sent only to URL's host, never logged.
	Header http.Header
}

// RawTool is a tool as the server reports it.
type RawTool struct {
	Name, Description string
	InputSchema       json.RawMessage
	// Annotations are the hints as reported; untrusted.
	Annotations json.RawMessage
	// TTLMs is the server's cache hint, nil when it sent none or 0.
	TTLMs *int
}

// ResumeMeta carries the state for retrying a call that returned InputRequired.
type ResumeMeta struct {
	// RequestState is opaque and echoed back verbatim.
	RequestState string
	// InputResponses are keyed by the server's own request key.
	InputResponses map[string]InputResponse
}

// InputResponse is the user's answer to one InputRequest.
type InputResponse struct {
	// Action is "accept", "decline" or "cancel".
	Action string
	// Content is set only when Action is "accept" and the request had a schema.
	Content json.RawMessage
}

// Result is what a tool call returned.
type Result struct {
	Content []msg.Part
	// IsError is MCP's isError: a normal tool error the model sees unchanged.
	IsError bool
	// InputRequired is set when the server wants input before it can answer.
	InputRequired *InputRequired
}

// InputRequired is a server's request for input, returned to the caller
// instead of being answered inside the client.
type InputRequired struct {
	// Requests are keyed by the server's own request key.
	Requests map[string]InputRequest
	// RequestState is opaque and stored as is.
	RequestState string
}

// InputRequest is one elicitation a server wants answered.
type InputRequest struct {
	// Mode is "form" or "url".
	Mode            string
	Message         string
	RequestedSchema json.RawMessage
	URL             string
}

// Client calls MCP servers through the guarded HTTP client.
type Client struct {
	http *http.Client
}

// New builds a Client; devAllowLocalhost is passed to NewHTTPClient.
func New(devAllowLocalhost bool) *Client {
	return &Client{http: NewHTTPClient(devAllowLocalhost)}
}

// ListTools returns every tool the server lists, and the era it answered in.
// Store the era in Target.Era to skip the probe on later calls.
func (c *Client) ListTools(ctx context.Context, t Target) ([]RawTool, Era, error) {
	cs, era, err := c.connect(ctx, t)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = cs.Close() }()
	var tools []RawTool
	params := &mcp.ListToolsParams{}
	for {
		res, err := cs.ListTools(ctx, params)
		if err != nil {
			return nil, "", fmt.Errorf("list tools: %w", err)
		}
		var ttl *int
		if res.TTLMs > 0 {
			ttl = &res.TTLMs
		}
		for _, tool := range res.Tools {
			raw := RawTool{Name: tool.Name, Description: tool.Description, TTLMs: ttl}
			if raw.InputSchema, err = json.Marshal(tool.InputSchema); err != nil {
				return nil, "", fmt.Errorf("encode schema of %q: %w", tool.Name, err)
			}
			if tool.Annotations != nil {
				if raw.Annotations, err = json.Marshal(tool.Annotations); err != nil {
					return nil, "", fmt.Errorf("encode annotations of %q: %w", tool.Name, err)
				}
			}
			tools = append(tools, raw)
		}
		if res.NextCursor == "" {
			break
		}
		params.Cursor = res.NextCursor
	}
	return tools, era, nil
}

// CallTool calls the tool name with args. meta is nil on a fresh call and
// carries the answers when retrying a call that returned InputRequired.
func (c *Client) CallTool(ctx context.Context, t Target, name string, args json.RawMessage, meta *ResumeMeta) (Result, error) {
	cs, _, err := c.connect(ctx, t)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = cs.Close() }()
	params := &mcp.CallToolParams{Name: name}
	if len(args) > 0 {
		params.Arguments = args
	}
	if meta != nil {
		params.RequestState = meta.RequestState
		if params.InputResponses, err = sdkResponses(meta.InputResponses); err != nil {
			return Result{}, err
		}
	}
	res, err := cs.CallTool(ctx, params)
	if err != nil {
		return Result{}, fmt.Errorf("call tool %q: %w", name, err)
	}
	return mapResult(res)
}

func (c *Client) connect(ctx context.Context, t Target) (*mcp.ClientSession, Era, error) {
	u, err := url.Parse(t.URL)
	if err != nil {
		return nil, "", fmt.Errorf("parse connector url: %w", err)
	}
	hc := *c.http
	cred := &credTransport{base: c.http.Transport, host: u.Host, header: t.Header}
	hc.Transport = cred
	client := mcp.NewClient(&mcp.Implementation{Name: "jelly-fish", Version: "0"}, &mcp.ClientOptions{
		Logger: slog.Default(),
		// The client declares elicitation only: never sampling or roots.
		Capabilities:   &mcp.ClientCapabilities{Elicitation: &mcp.ElicitationCapabilities{Form: &mcp.FormElicitationCapabilities{}, URL: &mcp.URLElicitationCapabilities{}}},
		MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
	})
	var opts *mcp.ClientSessionOptions
	if t.Era == EraLegacy {
		opts = &mcp.ClientSessionOptions{ProtocolVersion: legacyVersion}
	}
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: t.URL, HTTPClient: &hc, DisableStandaloneSSE: true}, opts)
	if err != nil {
		switch cred.status.Load() {
		case http.StatusBadRequest, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotAcceptable, http.StatusUnsupportedMediaType:
			return nil, "", fmt.Errorf("connect %s: %w: %w", u.Redacted(), ErrOutdatedServer, err)
		}
		return nil, "", fmt.Errorf("connect %s: %w", u.Redacted(), err)
	}
	if cs.InitializeResult().ProtocolVersion >= modernVersion {
		return cs, EraModern, nil
	}
	return cs, EraLegacy, nil
}

// credTransport adds the Connector's credential header to requests for its
// own host and remembers the last HTTP status, which tells an outdated server
// from an unreachable one.
type credTransport struct {
	base   http.RoundTripper
	host   string
	header http.Header
	status atomic.Int32
}

func (c *credTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == c.host {
		req = req.Clone(req.Context())
		for k, vs := range c.header {
			req.Header.Del(k)
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
	}
	resp, err := c.base.RoundTrip(req)
	if err == nil {
		c.status.Store(int32(resp.StatusCode))
	}
	return resp, err
}
