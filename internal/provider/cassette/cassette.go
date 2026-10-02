// Package cassette records a Provider's HTTP traffic once and replays it in
// tests, so adapter tests run without a key or the network. Set JF_RECORD=1
// (and the Provider's key) to re-record. Key and account-id headers are
// scrubbed before a cassette is written.
package cassette

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"
)

// Redacted replaces every scrubbed header's value.
const Redacted = "REDACTED"

// File is a cassette as stored.
type File struct {
	Interactions []Interaction `json:"interactions"`
}

// Interaction is one request and the response it got.
type Interaction struct {
	Request  Request  `json:"request"`
	Response Response `json:"response"`
}

// Request is the recorded part of an outgoing request.
type Request struct {
	Method string      `json:"method"`
	Path   string      `json:"path"`
	Header http.Header `json:"header"`
	Body   string      `json:"body"`
}

// Response is a recorded response.
type Response struct {
	Status int         `json:"status"`
	Header http.Header `json:"header"`
	Body   string      `json:"body"`
}

// Open replays path, or records into it through the network when
// JF_RECORD=1.
func Open(t testing.TB, path string) *http.Client {
	t.Helper()
	if Recording() {
		return Record(t, path, http.DefaultTransport)
	}
	return Replay(t, path)
}

// Recording reports whether JF_RECORD=1 asks for live recording.
func Recording() bool { return os.Getenv("JF_RECORD") == "1" }

// Key returns env's value when recording, and a placeholder when replaying,
// where no real key is needed.
func Key(t testing.TB, env string) string {
	t.Helper()
	if !Recording() {
		return "replay-key"
	}
	k := os.Getenv(env)
	if k == "" {
		t.Fatalf("JF_RECORD=1 needs %s", env)
	}
	return k
}

// Record sends through upstream and writes the scrubbed traffic to path when
// t ends.
func Record(t testing.TB, path string, upstream http.RoundTripper) *http.Client {
	t.Helper()
	r := &recorder{upstream: upstream}
	t.Cleanup(func() {
		b, err := json.MarshalIndent(File{Interactions: r.got}, "", "  ")
		if err != nil {
			t.Errorf("marshal cassette: %v", err)
			return
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Errorf("write cassette: %v", err)
		}
	})
	return &http.Client{Transport: r}
}

// Replay answers requests from path in recorded order. A request whose
// method or path differs from the next recorded one fails.
func Replay(t testing.TB, path string) *http.Client {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cassette: %v (record it with JF_RECORD=1)", err)
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("parse cassette %s: %v", path, err)
	}
	return &http.Client{Transport: &player{left: f.Interactions}}
}

type recorder struct {
	upstream http.RoundTripper
	mu       sync.Mutex
	got      []Interaction
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqBody []byte
	if req.Body != nil {
		var err error
		if reqBody, err = io.ReadAll(req.Body); err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}
	resp, err := r.upstream.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.got = append(r.got, Interaction{
		Request:  Request{Method: req.Method, Path: req.URL.Path, Header: scrub(req.Header), Body: string(reqBody)},
		Response: Response{Status: resp.StatusCode, Header: scrub(resp.Header), Body: string(respBody)},
	})
	r.mu.Unlock()

	resp.Body = io.NopCloser(bytes.NewReader(respBody))
	return resp, nil
}

// scrub blanks every Provider's key headers, and the account ids Anthropic
// echoes back.
func scrub(h http.Header) http.Header {
	h = h.Clone()
	for _, name := range []string{"x-api-key", "authorization", "x-goog-api-key", "anthropic-organization-id", "anthropic-workspace-id"} {
		if h.Get(name) != "" {
			h.Set(name, Redacted)
		}
	}
	return h
}

type player struct {
	mu   sync.Mutex
	left []Interaction
}

func (p *player) RoundTrip(req *http.Request) (*http.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.left) == 0 {
		return nil, fmt.Errorf("cassette: unexpected %s %s, nothing left to replay", req.Method, req.URL.Path)
	}
	in := p.left[0]
	if in.Request.Method != req.Method || in.Request.Path != req.URL.Path {
		return nil, fmt.Errorf("cassette: got %s %s, want %s %s", req.Method, req.URL.Path, in.Request.Method, in.Request.Path)
	}
	p.left = p.left[1:]
	return &http.Response{
		StatusCode: in.Response.Status,
		Status:     fmt.Sprintf("%d %s", in.Response.Status, http.StatusText(in.Response.Status)),
		Header:     in.Response.Header.Clone(),
		Body:       io.NopCloser(bytes.NewReader([]byte(in.Response.Body))),
		Request:    req,
	}, nil
}
