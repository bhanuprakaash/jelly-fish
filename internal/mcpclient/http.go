package mcpclient

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"time"
)

type lookupFunc func(ctx context.Context, network, host string) ([]netip.Addr, error)

// NewHTTPClient builds the one HTTP client for every Connector-bound call
// (docs/design/mcp-client.md §4.4). It requires https, refuses private,
// loopback and link-local addresses, and checks each hop of a redirect. The
// check runs on the IP the dialer connects to, so a DNS answer cannot be
// swapped after it. devAllowLocalhost also lets http://localhost and
// http://127.0.0.1 through.
func NewHTTPClient(devAllowLocalhost bool) *http.Client {
	return newHTTPClient(devAllowLocalhost, net.DefaultResolver.LookupNetIP)
}

func newHTTPClient(dev bool, lookup lookupFunc) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	base := &http.Transport{
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := lookup(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if err := checkIP(ip.Unmap(), dev); err != nil {
					return nil, fmt.Errorf("%s: %w", host, err)
				}
			}
			var errs []error
			for _, ip := range ips {
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				errs = append(errs, err)
			}
			return nil, errors.Join(errs...)
		},
	}
	return &http.Client{
		Transport: guardTransport{base: base, dev: dev},
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return checkURL(req.URL, dev)
		},
	}
}

type guardTransport struct {
	base http.RoundTripper
	dev  bool
}

func (g guardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := checkURL(req.URL, g.dev); err != nil {
		return nil, err
	}
	return g.base.RoundTrip(req)
}

func checkURL(u *url.URL, dev bool) error {
	if u.Scheme == "https" {
		return nil
	}
	if dev && u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1") {
		return nil
	}
	return fmt.Errorf("connector url %q must use https", u.Redacted())
}

func checkIP(ip netip.Addr, dev bool) error {
	if dev && ip.IsLoopback() {
		return nil
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("address %s is not allowed", ip)
	}
	denied := []string{"100.64.0.0/10", "64:ff9b::/96", "2002::/16", "fc00::/7"}
	if slices.ContainsFunc(denied, func(p string) bool { return netip.MustParsePrefix(p).Contains(ip) }) {
		return fmt.Errorf("address %s is not allowed", ip)
	}
	return nil
}
