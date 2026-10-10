package worker

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

// cancels holds the cancel func of every session this Worker drives, so a
// jf_cancel NOTIFY can stop one at once (event-log.md §5.8).
type cancels struct {
	mu  sync.Mutex
	fns map[uuid.UUID]context.CancelCauseFunc
}

func (c *cancels) add(sid uuid.UUID, cancel context.CancelCauseFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fns == nil {
		c.fns = map[uuid.UUID]context.CancelCauseFunc{}
	}
	c.fns[sid] = cancel
}

func (c *cancels) remove(sid uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.fns, sid)
}

// interrupt cancels sid with errInterrupted; a session this Worker does not
// hold is ignored.
func (c *cancels) interrupt(sid uuid.UUID) {
	c.mu.Lock()
	cancel := c.fns[sid]
	c.mu.Unlock()
	if cancel != nil {
		cancel(errInterrupted)
	}
}
