// Package mail sends transactional email over SMTP.
package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/url"
	"strings"
)

// Mailer sends one email (notifications.md §4.1). html may be empty.
type Mailer interface {
	Send(ctx context.Context, to, subject, text, html string) error
}

// SMTP is a Mailer that speaks SMTP to one server.
type SMTP struct {
	host string
	addr string
	from string
	auth smtp.Auth
}

var _ Mailer = (*SMTP)(nil)

// NewSMTP builds an SMTP Mailer from a URL like smtp://user:pass@host:587.
// Credentials are optional; STARTTLS is used when the server offers it.
func NewSMTP(rawURL, from string) (*SMTP, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "smtp" || u.Hostname() == "" {
		return nil, errors.New("smtp url: want smtp://[user:pass@]host[:port]")
	}
	port := u.Port()
	if port == "" {
		port = "25"
	}
	m := &SMTP{host: u.Hostname(), addr: net.JoinHostPort(u.Hostname(), port), from: from}
	if u.User != nil {
		pass, _ := u.User.Password()
		m.auth = smtp.PlainAuth("", u.User.Username(), pass, m.host)
	}
	return m, nil
}

// Send delivers the message, giving up when ctx is done.
func (m *SMTP) Send(ctx context.Context, to, subject, text, html string) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", m.addr)
	if err != nil {
		return fmt.Errorf("dial smtp: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(dl); err != nil {
			_ = conn.Close()
			return fmt.Errorf("set smtp deadline: %w", err)
		}
	}
	c, err := smtp.NewClient(conn, m.host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer func() { _ = c.Close() }()

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: m.host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if m.auth != nil {
		if err := c.Auth(m.auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(m.from); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt to: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write([]byte(message(m.from, to, subject, text, html))); err != nil {
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp end data: %w", err)
	}
	return c.Quit()
}

func message(from, to, subject, text, html string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\n", from, to, subject)
	if html == "" {
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
		b.WriteString(text)
		return b.String()
	}
	const boundary = "jf-alt"
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%s\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", boundary, text)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/html; charset=utf-8\r\n\r\n%s\r\n", boundary, html)
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.String()
}
