package blob_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/bhanuprakaash/jelly-fish/internal/blob"
	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

func TestSameContentIsOneRow(t *testing.T) {
	pool := testdb.NewPool(t)
	var refs []blob.Ref
	for range 2 {
		err := pgx.BeginFunc(t.Context(), pool, func(tx pgx.Tx) error {
			ref, err := blob.Put(t.Context(), tx, "text/plain", []byte("hello"))
			refs = append(refs, ref)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	const sha = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	want := blob.Ref{Key: sha, Size: 5, SHA256: sha, Mime: "text/plain"}
	if refs[0] != want || refs[1] != want {
		t.Fatalf("refs = %+v, want both %+v", refs, want)
	}
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM blobs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("blobs = %d, want 1", n)
	}
	mime, data, err := blob.Get(t.Context(), pool, sha)
	if err != nil || mime != "text/plain" || string(data) != "hello" {
		t.Fatalf("Get = %q, %q, %v", mime, data, err)
	}
}

func TestFailedAppendLeavesNoBlob(t *testing.T) {
	pool := testdb.NewPool(t)
	store := eventlog.NewStore(pool)
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool, "fake").CreateSession(t.Context(), testdb.NewUser(t, pool).Scope(), sid, uuid.New(), "hi", "", false); err != nil {
		t.Fatal(err)
	}
	c, _, err := store.Claim(t.Context(), "w1", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	stale := c.Fence
	stale.Epoch--
	_, err = store.AppendFencedFunc(t.Context(), sid, stale, func(ctx context.Context, tx pgx.Tx) ([]eventlog.NewEvent, error) {
		ref, err := blob.Put(ctx, tx, "text/plain", []byte("orphan"))
		return []eventlog.NewEvent{{Type: "x", Actor: "w", Payload: ref}}, err
	})
	if !errors.Is(err, eventlog.ErrLeaseLost) {
		t.Fatalf("err = %v, want ErrLeaseLost", err)
	}
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM blobs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("blobs = %d, want 0", n)
	}
}
