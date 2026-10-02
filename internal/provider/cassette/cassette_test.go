package cassette_test

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/provider/cassette"
)

const canary = "sk-canary-7f3a9b"

func TestRecordScrubsKeysAndReplays(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Authorization", "Bearer "+canary)
		w.Header().Set("Anthropic-Organization-Id", canary)
		w.Header().Set("Anthropic-Workspace-Id", canary)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "hello "+r.URL.Path)
	}))
	t.Cleanup(upstream.Close)
	path := filepath.Join(t.TempDir(), "c.json")

	send := func(t *testing.T, c *http.Client) string {
		t.Helper()
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, upstream.URL+"/v1/messages", strings.NewReader(`{"q":1}`))
		req.Header.Set("x-api-key", canary)
		req.Header.Set("Authorization", "Bearer "+canary)
		req.Header.Set("x-goog-api-key", canary)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	t.Run("record", func(t *testing.T) {
		if got := send(t, cassette.Record(t, path, http.DefaultTransport)); got != "hello /v1/messages" {
			t.Fatalf("recorded body = %q", got)
		}
	})

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), canary) {
		t.Fatalf("cassette contains the key:\n%s", b)
	}

	upstream.Close()
	t.Run("replay", func(t *testing.T) {
		if got := send(t, cassette.Replay(t, path)); got != "hello /v1/messages" {
			t.Fatalf("replayed body = %q", got)
		}
	})
}

// TestNoCassetteHoldsAKey scans every committed cassette for keys and
// account ids.
func TestNoCassetteHoldsAKey(t *testing.T) {
	n := 0
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".cassette.json") {
			return err
		}
		n++
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var c cassette.File
		if err := json.Unmarshal(b, &c); err != nil {
			t.Errorf("%s: %v", path, err)
			return nil
		}
		for i, in := range c.Interactions {
			for _, h := range []http.Header{in.Request.Header, in.Response.Header} {
				for _, name := range []string{"x-api-key", "authorization", "x-goog-api-key", "anthropic-organization-id", "anthropic-workspace-id"} {
					for _, v := range h.Values(name) {
						if v != cassette.Redacted {
							t.Errorf("%s interaction %d: %s is not redacted", path, i, name)
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no cassettes found; the scan is looking in the wrong place")
	}
}
