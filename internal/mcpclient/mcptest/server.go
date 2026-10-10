// Package mcptest is an in-process MCP server on the official SDK, for tests
// of code that calls MCP servers.
package mcptest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options selects the protocol era the server speaks.
type Options struct {
	// Legacy makes the server answer a modern request with a 400 and a body no
	// modern client recognizes, so a client must fall back to initialize.
	Legacy bool
	// TTLMs is the cache hint the server puts on its tool lists; 0 sends none.
	TTLMs int
}

// Server is a running test server; it stops when the test ends.
type Server struct {
	// URL is the MCP endpoint.
	URL string

	srv *mcp.Server

	mu       sync.Mutex
	methods  []string
	lastName string
	lastArgs json.RawMessage
	header   http.Header
	caps     json.RawMessage
}

// Start serves an MCP server with no tools on a loopback address.
func Start(t *testing.T, opts Options) *Server {
	s := &Server{srv: mcp.NewServer(&mcp.Implementation{Name: "mcptest", Version: "0"}, &mcp.ServerOptions{
		SetCacheable: func(_ context.Context, _ mcp.Request, c *mcp.Cacheable) { c.TTLMs = opts.TTLMs },
	})}
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.srv },
		&mcp.StreamableHTTPOptions{Stateless: !opts.Legacy})
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		method := s.record(r, body)
		if opts.Legacy && method == "server/discover" {
			http.Error(w, "unknown method", http.StatusBadRequest)
			return
		}
		mcpHandler.ServeHTTP(w, r)
	}))
	t.Cleanup(hs.Close)
	s.URL = hs.URL
	return s
}

// record notes a POSTed JSON-RPC request and returns its method.
func (s *Server) record(r *http.Request, body []byte) string {
	if r.Method != http.MethodPost {
		return ""
	}
	var req struct {
		Method string `json:"method"`
		Params struct {
			Name         string          `json:"name"`
			Arguments    json.RawMessage `json:"arguments"`
			Capabilities json.RawMessage `json:"capabilities"`
			Meta         struct {
				Capabilities json.RawMessage `json:"io.modelcontextprotocol/clientCapabilities"`
			} `json:"_meta"`
		} `json:"params"`
	}
	_ = json.Unmarshal(body, &req)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.methods = append(s.methods, req.Method)
	s.header = r.Header.Clone()
	if req.Params.Capabilities != nil {
		s.caps = req.Params.Capabilities
	}
	if req.Params.Meta.Capabilities != nil {
		s.caps = req.Params.Meta.Capabilities
	}
	if req.Method == "tools/call" {
		s.lastName, s.lastArgs = req.Params.Name, req.Params.Arguments
	}
	return req.Method
}

// Methods lists the JSON-RPC methods received so far, in order, including
// ones the server rejected.
func (s *Server) Methods() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.methods)
}

// LastCall is the name and arguments of the latest tools/call.
func (s *Server) LastCall() (name string, args json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastName, slices.Clone(s.lastArgs)
}

// Header is the header of the latest POST.
func (s *Server) Header() http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.header.Clone()
}

// ClientCapabilities is the capabilities object the client last declared.
func (s *Server) ClientCapabilities() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.caps)
}

// AddTool registers a tool that always returns result.
func (s *Server) AddTool(name string, result *mcp.CallToolResult) {
	s.addTool(name, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res := *result
		return &res, nil
	})
}

// AddLargeTool registers a tool that returns 100 KB of text.
func (s *Server) AddLargeTool(name string) {
	s.AddTool(name, &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", 100<<10)}}})
}

// AddInputRequiredTool registers a tool that first asks for input ("confirm",
// state "state-1") and, once retried with that state, returns "done".
func (s *Server) AddInputRequiredTool(name string) {
	s.addTool(name, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if req.Params.RequestState == "state-1" {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil
		}
		return &mcp.CallToolResult{
			InputRequests: mcp.InputRequestMap{"confirm": &mcp.ElicitParams{Message: "Proceed?"}},
			RequestState:  "state-1",
		}, nil
	})
}

func (s *Server) addTool(name string, h mcp.ToolHandler) {
	s.srv.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, h)
}
