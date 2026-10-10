package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/auth"
	"github.com/bhanuprakaash/jelly-fish/internal/connector"
	"github.com/bhanuprakaash/jelly-fish/internal/mcpclient"
)

// ConnectorConfig wires Connector settings into the server. The api role only
// seals a credential and probes with it; the Worker opens it.
type ConnectorConfig struct {
	Store  *connector.Store
	Client *mcpclient.Client
	Sealer Sealer
}

type connectorHandlers struct {
	cfg    ConnectorConfig
	logger *slog.Logger
}

type connectorToolJSON struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

type connectorJSON struct {
	ID         uuid.UUID           `json:"id"`
	Slug       string              `json:"slug"`
	Name       string              `json:"name"`
	URL        string              `json:"url"`
	AuthHeader string              `json:"auth_header"`
	Era        mcpclient.Era       `json:"era"`
	Tools      []connectorToolJSON `json:"tools"`
}

func toConnectorJSON(c connector.Connector, tools []connector.CachedTool) connectorJSON {
	out := connectorJSON{ID: c.ID, Slug: c.Slug, Name: c.Name, URL: c.URL, AuthHeader: c.AuthHeader, Era: c.Era, Tools: []connectorToolJSON{}}
	for _, t := range tools {
		out.Tools = append(out.Tools, connectorToolJSON{Name: t.Name, Description: t.Description, Enabled: t.Enabled})
	}
	return out
}

// project is the one Project the caller owns, which every route works in.
func (h *connectorHandlers) project(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	u, _ := auth.UserFrom(r.Context())
	id, err := h.cfg.Store.ProjectID(r.Context(), u.ID)
	if err != nil {
		h.logger.Error("find project", "user_id", u.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not find project")
		return uuid.Nil, false
	}
	return id, true
}

func (h *connectorHandlers) list(w http.ResponseWriter, r *http.Request) {
	projectID, ok := h.project(w, r)
	if !ok {
		return
	}
	conns, tools, err := h.cfg.Store.List(r.Context(), projectID)
	if err != nil {
		h.logger.Error("list connectors", "project_id", projectID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not list connectors")
		return
	}
	out := make([]connectorJSON, len(conns))
	for i, c := range conns {
		out[i] = toConnectorJSON(c, tools[c.ID])
	}
	writeJSON(w, http.StatusOK, out)
}

type probeRequest struct {
	URL        string `json:"url"`
	AuthHeader string `json:"auth_header"`
	Secret     string `json:"secret"`
	Era        string `json:"era"`
}

// listTimeout bounds the tools/list of a probe or create.
const listTimeout = 15 * time.Second

// validAuthHeader reports whether name is an HTTP token that is not a header
// the client sets itself.
func validAuthHeader(name string) bool {
	const token = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!#$%&'*+-.^_`|~"
	if name == "" || strings.Trim(name, token) != "" {
		return false
	}
	switch http.CanonicalHeaderKey(name) {
	case "Host", "Content-Type", "Content-Length", "Accept", "Mcp-Session-Id", "Mcp-Protocol-Version":
		return false
	}
	return true
}

// listTools asks the server for its tools, answering the request itself when
// that fails.
func (h *connectorHandlers) listTools(w http.ResponseWriter, r *http.Request, req probeRequest) ([]mcpclient.RawTool, mcpclient.Era, bool) {
	target := mcpclient.Target{URL: strings.TrimSpace(req.URL), Era: mcpclient.Era(req.Era)}
	switch {
	case req.Secret != "" && req.AuthHeader == "":
		writeError(w, http.StatusBadRequest, "auth_header is required with a secret")
		return nil, "", false
	case req.AuthHeader != "" && req.Secret == "":
		writeError(w, http.StatusBadRequest, "secret is required with auth_header")
		return nil, "", false
	case req.AuthHeader != "" && !validAuthHeader(req.AuthHeader):
		writeError(w, http.StatusBadRequest, "auth_header is not allowed")
		return nil, "", false
	case req.Secret != "":
		target.Header = http.Header{}
		target.Header.Set(req.AuthHeader, req.Secret)
	}
	ctx, cancel := context.WithTimeout(r.Context(), listTimeout)
	defer cancel()
	tools, era, err := h.cfg.Client.ListTools(ctx, target)
	switch {
	case errors.Is(err, mcpclient.ErrOutdatedServer):
		writeError(w, http.StatusUnprocessableEntity, "This server uses an outdated MCP version and can't be added")
	case err != nil && (strings.Contains(err.Error(), "is not allowed") || strings.Contains(err.Error(), "must use https")):
		writeError(w, http.StatusUnprocessableEntity, "This address can't be used")
	case err != nil:
		h.logger.Warn("probe connector", "error", err)
		writeError(w, http.StatusBadGateway, "could not reach the server")
	default:
		return tools, era, true
	}
	return nil, "", false
}

func (h *connectorHandlers) probe(w http.ResponseWriter, r *http.Request) {
	var req probeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	tools, era, ok := h.listTools(w, r, req)
	if !ok {
		return
	}
	out := struct {
		Era   mcpclient.Era       `json:"era"`
		Tools []connectorToolJSON `json:"tools"`
	}{Era: era, Tools: make([]connectorToolJSON, len(tools))}
	for i, t := range tools {
		out.Tools[i] = connectorToolJSON{Name: t.Name, Description: t.Description}
	}
	writeJSON(w, http.StatusOK, out)
}

type createConnectorRequest struct {
	probeRequest
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Tools []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	} `json:"tools"`
}

// create lists the server's tools again and keeps those the request enables:
// the stored definitions come from the server, never from the client.
func (h *connectorHandlers) create(w http.ResponseWriter, r *http.Request) {
	var req createConnectorRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || strings.TrimSpace(req.URL) == "" {
		writeError(w, http.StatusBadRequest, "name and url are required")
		return
	}
	projectID, ok := h.project(w, r)
	if !ok {
		return
	}
	raw, era, ok := h.listTools(w, r, req.probeRequest)
	if !ok {
		return
	}
	enabled := map[string]bool{}
	for _, t := range req.Tools {
		enabled[t.Name] = t.Enabled
	}
	tools := make([]connector.CachedTool, len(raw))
	for i, t := range raw {
		tools[i] = connector.FromRaw(t)
		tools[i].Enabled = enabled[t.Name]
	}

	c := connector.Connector{
		ID: uuid.New(), ProjectID: projectID, Slug: req.Slug, Name: req.Name,
		URL: strings.TrimSpace(req.URL), AuthHeader: req.AuthHeader, Era: era,
	}
	var sealed *connector.Sealed
	if req.Secret != "" {
		ct, keyID, err := h.cfg.Sealer.Seal([]byte(req.Secret), connector.AAD(projectID, c.ID))
		if err != nil {
			h.logger.Error("seal connector credential", "error", err)
			writeError(w, http.StatusInternalServerError, "could not save connector")
			return
		}
		sealed = &connector.Sealed{Ciphertext: ct, KeyID: keyID}
	}
	c, err := h.cfg.Store.Create(r.Context(), c, tools, sealed)
	if errors.Is(err, connector.ErrSlugTaken) {
		writeError(w, http.StatusConflict, "slug is taken")
		return
	}
	if err != nil {
		h.logger.Error("create connector", "project_id", projectID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not save connector")
		return
	}
	h.logger.Info("connector added", "project_id", projectID, "connector_id", c.ID)
	writeJSON(w, http.StatusCreated, toConnectorJSON(c, tools))
}

func (h *connectorHandlers) setToolEnabled(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "not found")
	if !ok {
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		writeError(w, http.StatusBadRequest, "enabled is required")
		return
	}
	projectID, ok := h.project(w, r)
	if !ok {
		return
	}
	h.finish(w, h.cfg.Store.SetToolEnabled(r.Context(), projectID, id, r.PathValue("name"), *req.Enabled), "update tool")
}

func (h *connectorHandlers) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "not found")
	if !ok {
		return
	}
	projectID, ok := h.project(w, r)
	if !ok {
		return
	}
	h.finish(w, h.cfg.Store.Delete(r.Context(), projectID, id), "delete connector")
}

// finish answers 204, or 404 for a Connector or tool that is not the
// caller's.
func (h *connectorHandlers) finish(w http.ResponseWriter, err error, what string) {
	switch {
	case errors.Is(err, connector.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case err != nil:
		h.logger.Error(what, "error", err)
		writeError(w, http.StatusInternalServerError, "could not "+what)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
