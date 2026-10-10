package mcpclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func get(t *testing.T, c *http.Client, url string) (*http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c.Do(req)
}

func TestGuardRejectsBeforeAnyConnection(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	lookup := func(_ context.Context, _, host string) ([]netip.Addr, error) {
		switch host {
		case "internal.example":
			return []netip.Addr{netip.MustParseAddr("10.0.0.5")}, nil
		case "rebind.example":
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		case "nat64.example":
			return []netip.Addr{netip.MustParseAddr("64:ff9b::a9fe:a9fe")}, nil
		}
		ip, err := netip.ParseAddr(host)
		return []netip.Addr{ip}, err
	}
	client := newHTTPClient(false, lookup)

	for name, c := range map[string]struct{ url, want string }{
		"loopback":             {fmt.Sprintf("https://127.0.0.1:%d", port), "not allowed"},
		"plain http":           {fmt.Sprintf("http://127.0.0.1:%d", port), "must use https"},
		"plain http, public":   {"http://example.com", "must use https"},
		"metadata address":     {"https://169.254.169.254/latest/meta-data", "not allowed"},
		"resolves to private":  {"https://internal.example", "not allowed"},
		"resolves to loopback": {fmt.Sprintf("https://rebind.example:%d", port), "not allowed"},
		"carrier-grade NAT":    {"https://100.64.0.1", "not allowed"},
		"resolves to NAT64":    {"https://nat64.example", "not allowed"},
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := get(t, client, c.url)
			if err == nil {
				_ = resp.Body.Close()
				t.Fatal("request was allowed")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
	if n := accepted.Load(); n != 0 {
		t.Fatalf("listener accepted %d connections", n)
	}
}

func TestGuardAllowsLoopbackInDev(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	t.Cleanup(hs.Close)

	resp, err := get(t, NewHTTPClient(true), hs.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestGuardRejectsRedirectToPrivateIP(t *testing.T) {
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://10.0.0.5/", http.StatusFound)
	}))
	t.Cleanup(hs.Close)

	resp, err := get(t, NewHTTPClient(true), hs.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("redirect was followed")
	}
	if !strings.Contains(err.Error(), "10.0.0.5 is not allowed") {
		t.Fatalf("got %v", err)
	}
}
