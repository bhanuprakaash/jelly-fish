package provider

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// ErrorKind is a neutral error class every adapter maps its failures onto
// (provider-gateway.md D9).
type ErrorKind string

// Error kinds. The stop classes (everything but RateLimited, LongWait and
// ProviderDown) need the User to act.
const (
	KindKeyInvalid       ErrorKind = "key_invalid"
	KindBilling          ErrorKind = "billing"
	KindModelUnavailable ErrorKind = "model_unavailable"
	KindRateLimited      ErrorKind = "rate_limited"
	KindLongWait         ErrorKind = "long_wait"
	KindProviderDown     ErrorKind = "provider_down"
	KindTooLarge         ErrorKind = "too_large"
	KindBug              ErrorKind = "bug"
)

// Retryable reports whether a quick in-process retry may clear the error.
func (k ErrorKind) Retryable() bool {
	return k == KindRateLimited || k == KindProviderDown
}

// Error is a classified Provider failure. It carries nothing the Provider
// sent back beyond the request id and status code, since Provider text can
// echo the request.
type Error struct {
	Kind ErrorKind
	// RetryAfter is how long the Provider asked us to wait; zero if it
	// didn't.
	RetryAfter time.Duration
	RequestID  string
	// HTTPStatus is the response status code; zero when there was no
	// response, such as a dropped connection or our own timeout.
	HTTPStatus int
	// Usage is what the failed attempt still consumed.
	Usage Usage
	Err   error
}

func (e *Error) Error() string {
	s := "provider error: " + string(e.Kind)
	if e.RequestID != "" {
		s += " (request " + e.RequestID + ")"
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// ErrStreamIdle means a response stream went silent past its idle timeout.
var ErrStreamIdle = errors.New("stream idle")

// IdleTimeout is how long a stream may send no bytes, pings included, before
// it is cut (provider-gateway.md D7).
const IdleTimeout = 300 * time.Second

// ShortWait is the longest retry-after worth an in-process retry; a longer
// one sleeps the session (provider-gateway.md D9).
const ShortWait = 60 * time.Second

// WaitKind is k, or KindLongWait when k is retryable but wait is longer than
// ShortWait: waiting in-process would hold the Lease that long.
func WaitKind(k ErrorKind, wait time.Duration) ErrorKind {
	if k.Retryable() && wait > ShortWait {
		return KindLongWait
	}
	return k
}

// RetryAfter is the wait resp's Retry-After header asks for in seconds, or
// zero.
func RetryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	secs, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// WatchedClient is a copy of hc (nil means a default client) with every
// response body under an idle watchdog of d (zero means IdleTimeout).
func WatchedClient(hc *http.Client, d time.Duration) *http.Client {
	out := http.Client{}
	if hc != nil {
		out = *hc
	}
	if out.Transport == nil {
		out.Transport = http.DefaultTransport
	}
	if d == 0 {
		d = IdleTimeout
	}
	out.Transport = WatchIdle(out.Transport, d)
	return &out
}

// WatchIdle wraps rt so a response body that returns no bytes for d is closed
// and its Read fails with ErrStreamIdle.
func WatchIdle(rt http.RoundTripper, d time.Duration) http.RoundTripper {
	return idleTransport{rt: rt, d: d}
}

type idleTransport struct {
	rt http.RoundTripper
	d  time.Duration
}

func (t idleTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.rt.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	b := &idleBody{ReadCloser: resp.Body, d: t.d}
	b.timer = time.AfterFunc(t.d, b.expire)
	resp.Body = b
	return resp, nil
}

type idleBody struct {
	io.ReadCloser
	d     time.Duration
	timer *time.Timer

	mu   sync.Mutex
	idle bool
}

func (b *idleBody) expire() {
	b.mu.Lock()
	b.idle = true
	b.mu.Unlock()
	_ = b.ReadCloser.Close()
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.timer.Reset(b.d)
	}
	if err != nil {
		b.mu.Lock()
		idle := b.idle
		b.mu.Unlock()
		if idle {
			return n, ErrStreamIdle
		}
		b.timer.Stop()
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	return b.ReadCloser.Close()
}
