// Package connector stores the remote MCP servers a Project uses and exposes
// their tools to the Worker (docs/design/mcp-client.md).
package connector

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/mcpclient"
)

const (
	separator = "__"
	// defaultTTL is how long cached tools stay fresh when the server sent no
	// hint (mcp-client.md §5.7).
	defaultTTL = 10 * time.Minute
)

// Connector is one remote MCP server attached to a Project.
type Connector struct {
	ID        uuid.UUID
	ProjectID uuid.UUID
	// Slug is unique within the Project and never changes after creation.
	Slug string
	Name string
	URL  string
	// AuthHeader names the header that carries the sealed credential, such as
	// Authorization or X-API-Key; empty when the server needs none.
	AuthHeader string
	Era        mcpclient.Era
}

// CachedTool is a tool as last listed by its server.
type CachedTool struct {
	Name, Description string
	InputSchema       json.RawMessage
	// Annotations are the server's hints, untrusted.
	Annotations json.RawMessage
	Enabled     bool
	TTLMs       *int
	// Hash is ToolHash of the definition the user approved.
	Hash      string
	FetchedAt time.Time
}

// Sealed is a credential as stored: the header value encrypted under the
// master key named KeyID.
type Sealed struct {
	Ciphertext []byte
	KeyID      string
}

// FromRaw turns a listed tool into a cache row that is enabled and hashed.
func FromRaw(r mcpclient.RawTool) CachedTool {
	return CachedTool{
		Name: r.Name, Description: r.Description, InputSchema: r.InputSchema, Annotations: r.Annotations,
		Enabled: true, TTLMs: r.TTLMs, Hash: ToolHash(r.Name, r.Description, r.InputSchema, r.Annotations),
	}
}

// PrefixedName is the tool name the model sees. The server only ever sees
// the unprefixed name.
func PrefixedName(slug, name string) string { return slug + separator + name }

// ValidName reports whether model APIs accept name as a tool name: 1 to 64
// letters, digits, underscores or hyphens. MCP also allows "." and "/".
func ValidName(name string) bool {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-"
	return len(name) > 0 && len(name) <= 64 && strings.Trim(name, chars) == ""
}

// StripPrefix reverses PrefixedName; ok is false when prefixed does not start
// with slug's prefix.
func StripPrefix(slug, prefixed string) (name string, ok bool) {
	return strings.CutPrefix(prefixed, slug+separator)
}

// ToolHash identifies a tool's definition: sha256 over its canonical JSON.
func ToolHash(name, description string, inputSchema, annotations json.RawMessage) string {
	b, _ := json.Marshal(struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema"`
		Annotations json.RawMessage `json:"annotations"`
	}{name, description, canonical(inputSchema), canonical(annotations)})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// canonical rewrites raw with sorted keys and no spaces, so the hash survives
// the round trip through jsonb.
func canonical(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

// AAD binds a credential to its Project and Connector, so a ciphertext copied
// to another row fails to open (auth-keys.md §3).
func AAD(projectID, connectorID uuid.UUID) []byte {
	return []byte(projectID.String() + "|" + connectorID.String())
}

// Stale reports whether t is older than its server's TTL hint, or ten
// minutes without one.
func Stale(t CachedTool, now time.Time) bool {
	ttl := defaultTTL
	if t.TTLMs != nil {
		ttl = time.Duration(*t.TTLMs) * time.Millisecond
	}
	return now.Sub(t.FetchedAt) > ttl
}
