package connector

import (
	"context"
	"fmt"
	"net/http"

	"github.com/bhanuprakaash/jelly-fish/internal/mcpclient"
	"github.com/bhanuprakaash/jelly-fish/internal/tool"
)

// Tool is one cached tool of a Connector, callable by the model.
type Tool struct {
	Conn   Connector
	Cached CachedTool
	Client *mcpclient.Client
	// Header carries the opened credential, if the Connector has one.
	Header http.Header
}

var _ tool.Tool = Tool{}

// Def hides the server's hints: a custom-URL Connector's tools are neither
// read-only, destructive nor parallel-safe, and their output is untrusted.
func (t Tool) Def() tool.Def {
	return tool.Def{
		Name:        PrefixedName(t.Conn.Slug, t.Cached.Name),
		Description: t.Cached.Description,
		Schema:      t.Cached.InputSchema,
		Source:      "connector:" + t.Conn.ID.String(),
		Untrusted:   true,
	}
}

// CurrentHash is ToolHash of the definition the server last listed. It differs
// from Cached.Hash once the tool has changed since the User approved it.
func (t Tool) CurrentHash() string {
	return ToolHash(t.Cached.Name, t.Cached.Description, t.Cached.InputSchema, t.Cached.Annotations)
}

// Call calls the tool under the name its server gave it. A server that cannot
// be reached is an error Result the model sees, not an error.
func (t Tool) Call(ctx context.Context, in tool.CallInput) (tool.Result, error) {
	res, err := t.Client.CallTool(ctx, mcpclient.Target{URL: t.Conn.URL, Era: t.Conn.Era, Header: t.Header}, t.Cached.Name, in.Args, nil)
	if err != nil {
		return tool.TextResult(fmt.Sprintf("%s unreachable: %v", t.Conn.Name, err), true), nil
	}
	if res.InputRequired != nil {
		return tool.TextResult("needs user input; not supported yet", true), nil
	}
	return tool.Result{Content: res.Content, IsError: res.IsError}, nil
}
