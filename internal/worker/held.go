package worker

import (
	"encoding/json"
	"sync"

	"github.com/google/uuid"
)

// held keeps the real args of memory calls, whose stored args are redacted
// (memory.md D28), until the drive that received them runs the call. A call
// that outlives its drive finds nothing here and is answered with an error.
type held struct {
	mu   sync.Mutex
	args map[heldKey]json.RawMessage
}

type heldKey struct {
	session uuid.UUID
	call    string
}

func (h *held) put(sid uuid.UUID, call string, args json.RawMessage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.args == nil {
		h.args = map[heldKey]json.RawMessage{}
	}
	h.args[heldKey{sid, call}] = args
}

func (h *held) take(sid uuid.UUID, call string) (json.RawMessage, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	k := heldKey{sid, call}
	args, ok := h.args[k]
	delete(h.args, k)
	return args, ok
}

func (h *held) drop(sid uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k := range h.args {
		if k.session == sid {
			delete(h.args, k)
		}
	}
}
