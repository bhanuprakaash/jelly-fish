package worker

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/connector"
	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/mcpclient"
	"github.com/bhanuprakaash/jelly-fish/internal/tool"
)

// relistTimeout bounds one tools/list refresh, so a slow server cannot hold
// up the drive.
const relistTimeout = 15 * time.Second

// registry is the tools the claimed session can call: the built-ins plus the
// enabled tools of its Project's Connectors. A Connector whose cached tools
// are stale is listed again first; one that cannot be listed keeps its cache
// until its TTL passes again.
func (w *Worker) registry(ctx context.Context, c eventlog.Claim) (*tool.Registry, error) {
	if w.connectors == nil {
		return w.tools, nil
	}
	conns, cached, err := w.connectors.ToolsFor(ctx, c.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("load connectors: %w", err)
	}
	headers := make(map[uuid.UUID]http.Header, len(conns))
	relisted := false
	for _, conn := range conns {
		headers[conn.ID] = w.credentialHeader(ctx, conn)
		if slices.ContainsFunc(cached[conn.ID], func(t connector.CachedTool) bool { return connector.Stale(t, time.Now()) }) {
			relisted = w.relist(ctx, conn, headers[conn.ID]) || relisted
		}
	}
	if relisted {
		if conns, cached, err = w.connectors.ToolsFor(ctx, c.ProjectID); err != nil {
			return nil, fmt.Errorf("load connectors: %w", err)
		}
	}
	var tools []tool.Tool
	var skipped []string
	for _, conn := range conns {
		for _, t := range cached[conn.ID] {
			ct := connector.Tool{Conn: conn, Cached: t, Client: w.mcp, Header: headers[conn.ID]}
			if name := ct.Def().Name; !connector.ValidName(name) {
				skipped = append(skipped, name)
				continue
			}
			tools = append(tools, ct)
		}
	}
	if len(skipped) > 0 {
		w.logger.Warn("connector tools with names models reject are not offered", "session_id", c.SessionID, "tools", skipped)
	}
	return w.tools.With(tools...), nil
}

// relist replaces conn's cached tools with the server's current list and
// reports whether it did.
func (w *Worker) relist(ctx context.Context, conn connector.Connector, header http.Header) bool {
	ctx, cancel := context.WithTimeout(ctx, relistTimeout)
	defer cancel()
	raw, era, err := w.mcp.ListTools(ctx, mcpclient.Target{URL: conn.URL, Era: conn.Era, Header: header})
	if err != nil {
		w.logger.Warn("relist connector tools", "connector_id", conn.ID, "error", err)
		if err := w.connectors.Touch(ctx, conn.ID); err != nil {
			w.logger.Warn("touch connector tools", "connector_id", conn.ID, "error", err)
		}
		return false
	}
	tools := make([]connector.CachedTool, len(raw))
	for i, r := range raw {
		tools[i] = connector.FromRaw(r)
	}
	if err := w.connectors.Replace(ctx, conn.ID, tools); err != nil {
		w.logger.Warn("store connector tools", "connector_id", conn.ID, "error", err)
		return false
	}
	if era != conn.Era {
		if err := w.connectors.SetEra(ctx, conn.ID, era); err != nil {
			w.logger.Warn("store connector era", "connector_id", conn.ID, "error", err)
		}
	}
	return true
}

// credentialHeader opens conn's credential into the header it travels in. A
// credential that cannot be opened leaves the call to fail at the server.
func (w *Worker) credentialHeader(ctx context.Context, conn connector.Connector) http.Header {
	if conn.AuthHeader == "" || w.gateway.Keyring == nil {
		return nil
	}
	sealed, err := w.connectors.Credential(ctx, conn.ID)
	if err != nil {
		w.logger.Warn("load connector credential", "connector_id", conn.ID, "error", err)
		return nil
	}
	secret, err := w.gateway.Keyring.Open(sealed.Ciphertext, sealed.KeyID, connector.AAD(conn.ProjectID, conn.ID))
	if err != nil {
		w.logger.Warn("open connector credential", "connector_id", conn.ID, "error", err)
		return nil
	}
	h := http.Header{}
	h.Set(conn.AuthHeader, string(secret))
	return h
}
