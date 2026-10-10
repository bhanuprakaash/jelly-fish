package connector_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/connector"
	"github.com/bhanuprakaash/jelly-fish/internal/keyring"
	"github.com/bhanuprakaash/jelly-fish/internal/mcpclient"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

func TestPrefixedName(t *testing.T) {
	if got := connector.PrefixedName("notion", "search_pages"); got != "notion__search_pages" {
		t.Fatalf("PrefixedName = %q", got)
	}
	if name, ok := connector.StripPrefix("notion", "notion__search_pages"); !ok || name != "search_pages" {
		t.Fatalf("StripPrefix = %q, %v", name, ok)
	}
	if name, ok := connector.StripPrefix("linear", "notion__search_pages"); ok {
		t.Fatalf("StripPrefix with the wrong slug = %q, true", name)
	}
}

func TestToolHashIgnoresKeyOrderAndSpaces(t *testing.T) {
	a := connector.ToolHash("t", "d", json.RawMessage(`{"a": 1, "b": 2}`), nil)
	b := connector.ToolHash("t", "d", json.RawMessage(`{"b":2,"a":1}`), nil)
	if a != b {
		t.Fatalf("hashes differ: %s vs %s", a, b)
	}
	if c := connector.ToolHash("t", "changed", json.RawMessage(`{"a":1,"b":2}`), nil); c == a {
		t.Fatal("a changed description kept the hash")
	}
}

func TestStale(t *testing.T) {
	now := time.Now()
	short := 1000
	for name, tc := range map[string]struct {
		tool connector.CachedTool
		want bool
	}{
		"fresh under the default":  {connector.CachedTool{FetchedAt: now.Add(-9 * time.Minute)}, false},
		"old past the default":     {connector.CachedTool{FetchedAt: now.Add(-11 * time.Minute)}, true},
		"old past the server hint": {connector.CachedTool{FetchedAt: now.Add(-2 * time.Second), TTLMs: &short}, true},
	} {
		if got := connector.Stale(tc.tool, now); got != tc.want {
			t.Errorf("%s: Stale = %v, want %v", name, got, tc.want)
		}
	}
}

func TestCreateAllocatesSlugs(t *testing.T) {
	pool := testdb.NewPool(t)
	store := connector.NewStore(pool)
	projectID, err := store.ProjectID(t.Context(), testdb.NewUser(t, pool).ID)
	if err != nil {
		t.Fatal(err)
	}
	add := func(name, slug string) (connector.Connector, error) {
		return store.Create(t.Context(), connector.Connector{ProjectID: projectID, Name: name, Slug: slug, URL: "https://x.test/mcp", Era: mcpclient.EraModern}, nil, nil)
	}
	for _, want := range []string{"notion", "notion2", "notion3"} {
		c, err := add("Notion", "")
		if err != nil || c.Slug != want {
			t.Fatalf("slug = %q, %v; want %q", c.Slug, err, want)
		}
	}
	if _, err := add("Mine", "notion"); !errors.Is(err, connector.ErrSlugTaken) {
		t.Fatalf("taken chosen slug: err = %v, want ErrSlugTaken", err)
	}
	if c, err := add("My Linear!", ""); err != nil || c.Slug != "mylinear" {
		t.Fatalf("slug = %q, %v; want mylinear", c.Slug, err)
	}
}

func TestSealedCredentialOpensWithTheKeyring(t *testing.T) {
	pool := testdb.NewPool(t)
	kr, err := keyring.Parse("m1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	store := connector.NewStore(pool)
	projectID, err := store.ProjectID(t.Context(), testdb.NewUser(t, pool).ID)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	ct, keyID, err := kr.Seal([]byte("Bearer s3cret"), connector.AAD(projectID, id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(t.Context(), connector.Connector{ID: id, ProjectID: projectID, Name: "Notion", URL: "https://x.test/mcp", AuthHeader: "Authorization", Era: mcpclient.EraModern},
		nil, &connector.Sealed{Ciphertext: ct, KeyID: keyID}); err != nil {
		t.Fatal(err)
	}

	sealed, err := store.Credential(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	pt, err := kr.Open(sealed.Ciphertext, sealed.KeyID, connector.AAD(projectID, id))
	if err != nil || string(pt) != "Bearer s3cret" {
		t.Fatalf("opened %q, %v", pt, err)
	}
	var issuer *string
	var rowKeyID string
	if err := pool.QueryRow(t.Context(), `SELECT issuer, key_id FROM connector_credentials WHERE connector_id = $1`, id).Scan(&issuer, &rowKeyID); err != nil {
		t.Fatal(err)
	}
	if issuer != nil || rowKeyID != kr.Primary() {
		t.Fatalf("issuer = %v, key_id = %q; want NULL and %q", issuer, rowKeyID, kr.Primary())
	}
}
