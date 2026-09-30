package api

import (
	"sync"

	"github.com/google/uuid"
)

// maxStreamsPerUser caps one user's open streams on this node; the next gets
// 429, which also stops EventSource retrying (streaming.md D13).
const maxStreamsPerUser = 20

// streamLimiter counts each user's open streams on this node.
type streamLimiter struct {
	limit int

	mu   sync.Mutex
	open map[uuid.UUID]int
}

func newStreamLimiter(limit int) *streamLimiter {
	return &streamLimiter{limit: limit, open: make(map[uuid.UUID]int)}
}

// acquire claims a stream slot for user. It reports false when user is at the
// cap; otherwise call release when the stream closes.
func (l *streamLimiter) acquire(user uuid.UUID) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open[user] >= l.limit {
		return nil, false
	}
	l.open[user]++
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if l.open[user]--; l.open[user] == 0 {
			delete(l.open, user)
		}
	}, true
}
