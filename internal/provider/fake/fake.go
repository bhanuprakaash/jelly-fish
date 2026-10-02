// Package fake implements a Provider that echoes the last user message, for
// local and deployed dev use. It is wired in only under the dev build tag.
package fake

import (
	"cmp"
	"context"
	"strings"
	"time"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

const (
	// Name is the provider label recorded for fake replies.
	Name = "fake"

	defaultWordDelay = 150 * time.Millisecond
	// defaultMinReply keeps replies long enough to watch streaming and
	// interrupts.
	defaultMinReply = 5 * time.Second
	slowPrefix      = "/slow "
)

// Provider echoes the last user message word by word.
type Provider struct {
	// WordDelay is the pause between words; zero means 150 ms.
	WordDelay time.Duration
	// MinReply is the shortest reply duration; zero means 5 s.
	MinReply time.Duration
}

var _ provider.Provider = Provider{}

// Name implements provider.Provider.
func (Provider) Name() string { return Name }

// Stream implements provider.Provider. A "/slow 30s" prefix on the last user
// message stretches the reply over that duration.
func (p Provider) Stream(ctx context.Context, req provider.Request, onDelta func(provider.Delta)) (provider.Response, error) {
	delay := cmp.Or(p.WordDelay, defaultWordDelay)
	total := cmp.Or(p.MinReply, defaultMinReply)

	text := lastUserText(req.Messages)
	d, rest, slow := parseSlow(text)
	if slow {
		total, text = d, rest
	}

	echo := append([]string{"echo:"}, strings.Fields(text)...)
	count := max(len(echo), int(total/delay))
	reply := make([]string, count)
	for i := range reply {
		reply[i] = echo[i%len(echo)]
	}
	if slow {
		delay = total / time.Duration(count)
	}

	var out strings.Builder
	for i, w := range reply {
		select {
		case <-ctx.Done():
			return provider.Response{}, ctx.Err()
		case <-time.After(delay):
		}
		if i > 0 {
			w = " " + w
		}
		out.WriteString(w)
		onDelta(provider.Delta{Text: w})
	}

	return provider.Response{
		Message:    msg.AssistantText(out.String()),
		StopReason: provider.StopReasonEndTurn,
		Usage: provider.Usage{
			Input:  wordCount(req.Messages),
			Output: int64(len(reply)),
		},
	}, nil
}

func lastUserText(messages []msg.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == msg.RoleUser {
			return messages[i].Text()
		}
	}
	return ""
}

func wordCount(messages []msg.Message) int64 {
	var n int64
	for _, m := range messages {
		n += int64(len(strings.Fields(m.Text())))
	}
	return n
}

// parseSlow splits "/slow 30s rest of text" into its duration and the rest.
func parseSlow(text string) (time.Duration, string, bool) {
	rest, ok := strings.CutPrefix(text, slowPrefix)
	if !ok {
		return 0, "", false
	}
	arg, rest, _ := strings.Cut(strings.TrimSpace(rest), " ")
	d, err := time.ParseDuration(arg)
	if err != nil || d <= 0 {
		return 0, "", false
	}
	return d, rest, true
}
