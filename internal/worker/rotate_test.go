package worker

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/keyring"
	"github.com/bhanuprakaash/jelly-fish/internal/providerkeys"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

const (
	keyM1 = "m1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	keyM2 = "m2:AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="
)

func parseKeyring(t *testing.T, env string) *keyring.Keyring {
	t.Helper()
	kr, err := keyring.Parse(env)
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

func insertSealed(t *testing.T, pool *pgxpool.Pool, kr *keyring.Keyring, userID uuid.UUID, provider, plaintext string) {
	t.Helper()
	ct, keyID, err := kr.Seal([]byte(plaintext), providerkeys.AAD(userID, provider))
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO provider_keys (user_id, provider, ciphertext, key_id, last4, models, models_fetched_at)
		VALUES ($1, $2, $3, $4, 'last', '[]', now())`, userID, provider, ct, keyID)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRotateKeysMovesEveryRowToThePrimary(t *testing.T) {
	pool := testdb.NewPool(t)
	old := parseKeyring(t, keyM1)
	u1, u2 := testdb.NewUser(t, pool), testdb.NewUser(t, pool)
	insertSealed(t, pool, old, u1.ID, "anthropic", "key-one")
	insertSealed(t, pool, old, u2.ID, "anthropic", "key-two")

	rotated := parseKeyring(t, keyM2+","+keyM1)
	insertSealed(t, pool, rotated, u2.ID, "openai", "already-new")

	n, err := RotateKeys(t.Context(), providerkeys.NewStore(pool), rotated)
	if err != nil || n != 2 {
		t.Fatalf("RotateKeys = %d, %v; want 2, nil", n, err)
	}

	var stale int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM provider_keys WHERE key_id = 'm1'`).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("rows still under m1 = %d, err %v", stale, err)
	}
	onlyNew := parseKeyring(t, keyM2)
	for _, want := range []struct {
		user     uuid.UUID
		provider string
		key      string
	}{{u1.ID, "anthropic", "key-one"}, {u2.ID, "anthropic", "key-two"}, {u2.ID, "openai", "already-new"}} {
		var ct []byte
		var keyID string
		if err := pool.QueryRow(t.Context(), `SELECT ciphertext, key_id FROM provider_keys WHERE user_id = $1 AND provider = $2`, want.user, want.provider).Scan(&ct, &keyID); err != nil {
			t.Fatal(err)
		}
		got, err := onlyNew.Open(ct, keyID, providerkeys.AAD(want.user, want.provider))
		if err != nil || string(got) != want.key {
			t.Errorf("%s: Open = %q, %v; want %q", want.provider, got, err, want.key)
		}
	}

	if n, err := RotateKeys(t.Context(), providerkeys.NewStore(pool), rotated); err != nil || n != 0 {
		t.Errorf("second RotateKeys = %d, %v; want 0, nil", n, err)
	}
}

func TestSealedKeyCopiedToAnotherUserFailsToOpen(t *testing.T) {
	pool := testdb.NewPool(t)
	kr := parseKeyring(t, keyM1)
	owner, thief := testdb.NewUser(t, pool), testdb.NewUser(t, pool)
	insertSealed(t, pool, kr, owner.ID, "anthropic", "secret")

	if _, err := pool.Exec(t.Context(), `UPDATE provider_keys SET user_id = $1`, thief.ID); err != nil {
		t.Fatal(err)
	}
	var ct []byte
	var keyID string
	if err := pool.QueryRow(t.Context(), `SELECT ciphertext, key_id FROM provider_keys WHERE user_id = $1`, thief.ID).Scan(&ct, &keyID); err != nil {
		t.Fatal(err)
	}
	if _, err := kr.Open(ct, keyID, providerkeys.AAD(thief.ID, "anthropic")); err == nil {
		t.Fatal("Open succeeded for a row copied to another user_id")
	}
}

func TestRotateKeysSkipsARowItCannotOpen(t *testing.T) {
	pool := testdb.NewPool(t)
	old := parseKeyring(t, keyM1)
	u1, bad, u3 := testdb.NewUser(t, pool), testdb.NewUser(t, pool), testdb.NewUser(t, pool)
	ct, keyID, err := old.Seal([]byte("stolen"), providerkeys.AAD(u1.ID, "openai"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO provider_keys (user_id, provider, ciphertext, key_id, last4, models, models_fetched_at)
		VALUES ($1, 'anthropic', $2, $3, 'last', '[]', now())`, bad.ID, ct, keyID); err != nil {
		t.Fatal(err)
	}
	insertSealed(t, pool, old, u1.ID, "anthropic", "key-one")
	insertSealed(t, pool, old, u3.ID, "anthropic", "key-three")

	n, err := RotateKeys(t.Context(), providerkeys.NewStore(pool), parseKeyring(t, keyM2+","+keyM1))
	if n != 2 || err == nil {
		t.Fatalf("RotateKeys = %d, %v; want 2 and an error for the bad row", n, err)
	}
	for user, want := range map[uuid.UUID]string{u1.ID: "m2", u3.ID: "m2", bad.ID: "m1"} {
		var got string
		if err := pool.QueryRow(t.Context(), `SELECT key_id FROM provider_keys WHERE user_id = $1`, user).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("user %s key_id = %s, want %s", user, got, want)
		}
	}
}
