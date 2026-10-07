package api

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/bhanuprakaash/jelly-fish/internal/memory"
)

// memoryHandlers serves the Memories page and the memory chips: changes the
// user makes by hand, which make no LLM call and no event.
type memoryHandlers struct {
	pages  *memory.Pages
	logger *slog.Logger
}

// fail answers err for a memory request: 404, 409 and 422 for the page's own
// errors, else 500.
func (h *memoryHandlers) fail(w http.ResponseWriter, err error, what string) {
	var rejected memory.RejectedError
	switch {
	case errors.Is(err, memory.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, memory.ErrChanged):
		writeError(w, http.StatusConflict, "changed since")
	case errors.As(err, &rejected):
		writeError(w, http.StatusUnprocessableEntity, rejected.Reason)
	default:
		h.logger.Error(what, "error", err)
		writeError(w, http.StatusInternalServerError, "could not "+what)
	}
}

func (h *memoryHandlers) list(w http.ResponseWriter, r *http.Request) {
	o, err := h.pages.List(r.Context(), scopeFrom(r))
	if err != nil {
		h.fail(w, err, "list memories")
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (h *memoryHandlers) revisions(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "not found")
	if !ok {
		return
	}
	revs, err := h.pages.Revisions(r.Context(), scopeFrom(r), id)
	if err != nil {
		h.fail(w, err, "list revisions")
		return
	}
	writeJSON(w, http.StatusOK, revs)
}

type editMemoryRequest struct {
	Content string `json:"content"`
	Version int    `json:"version"`
}

func (h *memoryHandlers) edit(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "not found")
	if !ok {
		return
	}
	var req editMemoryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	h.done(w, h.pages.Edit(r.Context(), scopeFrom(r), id, req.Version, req.Content), "edit memory")
}

func (h *memoryHandlers) approve(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "not found")
	if !ok {
		return
	}
	var req versionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	h.done(w, h.pages.Approve(r.Context(), scopeFrom(r), id, req.Version), "approve memory")
}

func (h *memoryHandlers) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "not found")
	if !ok {
		return
	}
	h.done(w, h.pages.Delete(r.Context(), scopeFrom(r), id), "delete memory")
}

type versionRequest struct {
	Version int `json:"version"`
}

func (h *memoryHandlers) undo(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "not found")
	if !ok {
		return
	}
	var req versionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Version < 1 {
		writeError(w, http.StatusBadRequest, "version is required")
		return
	}
	h.done(w, h.pages.Undo(r.Context(), scopeFrom(r), id, req.Version), "undo memory")
}

type patchProjectRequest struct {
	UseUserMemory *bool `json:"use_user_memory"`
}

func (h *memoryHandlers) patchProject(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "not found")
	if !ok {
		return
	}
	var req patchProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.UseUserMemory == nil {
		writeError(w, http.StatusBadRequest, "use_user_memory is required")
		return
	}
	h.done(w, h.pages.SetUseUserMemory(r.Context(), scopeFrom(r), id, *req.UseUserMemory), "update project")
}

func (h *memoryHandlers) done(w http.ResponseWriter, err error, what string) {
	if err != nil {
		h.fail(w, err, what)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
