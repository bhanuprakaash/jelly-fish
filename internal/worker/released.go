package worker

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// releaseBackoff is how long a Worker leaves a session it released alone. Each
// claim appends an event, so re-claiming at once would flood the log while no
// compatible Worker has arrived; waiting a minute bounds that to one event a
// minute per session.
const releaseBackoff = time.Minute

// released remembers sessions this Worker handed back because it cannot read
// them. The zero value is ready to use.
type released struct {
	mu    sync.Mutex
	until map[uuid.UUID]time.Time
}

func (r *released) add(sid uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.until == nil {
		r.until = make(map[uuid.UUID]time.Time)
	}
	r.until[sid] = time.Now().Add(releaseBackoff)
}

// ids returns the sessions still in their backoff, forgetting the rest.
func (r *released) ids() []uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	var out []uuid.UUID
	for sid, until := range r.until {
		if now.After(until) {
			delete(r.until, sid)
			continue
		}
		out = append(out, sid)
	}
	return out
}
