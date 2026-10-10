package api

import (
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/bhanuprakaash/jelly-fish/internal/blob"
	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

func TestGetBlobIsScopedToTheSessionThatReferencesIt(t *testing.T) {
	e := newLoginEnv(t)
	alice := testdb.NewUser(t, e.pool)
	ac := e.signInUser(t, alice)
	bc := e.signInUser(t, testdb.NewUser(t, e.pool))
	chat := e.newChat(t, ac, "hello")

	var used, unused blob.Ref
	err := pgx.BeginFunc(t.Context(), e.pool, func(tx pgx.Tx) error {
		var err error
		if used, err = blob.Put(t.Context(), tx, "application/json", []byte(`[{"k":"text"}]`)); err != nil {
			return err
		}
		unused, err = blob.Put(t.Context(), tx, "text/plain", []byte("nobody points here"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = eventlog.NewStore(e.pool).Append(t.Context(), chat, nil, []eventlog.NewEvent{{
		Type: eventlog.TypeToolCompleted, Actor: "worker", Payload: map[string]any{"tool_call_id": "a", "blob_ref": used},
	}, {
		Type: eventlog.TypeToolCompleted, Actor: "worker", Payload: map[string]any{"tool_call_id": "b", "preview": "sha " + unused.SHA256},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	path := "/api/sessions/" + chat.String() + "/blobs/"
	rr := e.do(http.MethodGet, path+used.SHA256, nil, ac)
	if rr.Code != http.StatusOK || rr.Body.String() != `[{"k":"text"}]` || rr.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("own blob: %d %q %q", rr.Code, rr.Body, rr.Header().Get("Content-Type"))
	}
	for name, c := range map[string]struct {
		sha    string
		cookie *http.Cookie
	}{
		"sha only in a result's text": {unused.SHA256, ac},
		"another user":                {used.SHA256, bc},
	} {
		if rr := e.do(http.MethodGet, path+c.sha, nil, c.cookie); rr.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", name, rr.Code)
		}
	}
}
