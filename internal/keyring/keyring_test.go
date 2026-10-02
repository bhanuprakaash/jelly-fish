package keyring

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func b64Key(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func TestSealOpenRoundTrip(t *testing.T) {
	kr, err := Parse("m1:" + b64Key(t))
	if err != nil {
		t.Fatal(err)
	}
	ct, id, err := kr.Seal([]byte("secret"), []byte("u|anthropic"))
	if err != nil {
		t.Fatal(err)
	}
	if id != "m1" {
		t.Errorf("keyID = %q, want m1", id)
	}
	got, err := kr.Open(ct, id, []byte("u|anthropic"))
	if err != nil || !bytes.Equal(got, []byte("secret")) {
		t.Fatalf("Open = %q, %v", got, err)
	}
}

func TestOpenFails(t *testing.T) {
	kr, err := Parse("m1:" + b64Key(t) + ",m0:" + b64Key(t))
	if err != nil {
		t.Fatal(err)
	}
	aad := []byte("u|anthropic")
	ct, id, err := kr.Seal([]byte("secret"), aad)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Clone(ct)
	tampered[len(tampered)-1] ^= 1

	tests := []struct {
		name  string
		ct    []byte
		keyID string
		aad   []byte
	}{
		{"wrong aad", ct, id, []byte("other|anthropic")},
		{"unknown key id", ct, "nope", aad},
		{"other known key", ct, "m0", aad},
		{"tampered", tampered, id, aad},
		{"short", ct[:5], id, aad},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := kr.Open(tt.ct, tt.keyID, tt.aad); err == nil {
				t.Fatal("Open succeeded")
			}
		})
	}
}

func TestSealUsesPrimaryAndFreshNonce(t *testing.T) {
	kr, err := Parse("m2:" + b64Key(t) + ",m1:" + b64Key(t))
	if err != nil {
		t.Fatal(err)
	}
	a, id, _ := kr.Seal([]byte("x"), nil)
	b, _, _ := kr.Seal([]byte("x"), nil)
	if id != "m2" || kr.Primary() != "m2" {
		t.Errorf("primary = %q / %q, want m2", id, kr.Primary())
	}
	if bytes.Equal(a, b) {
		t.Error("two seals of the same plaintext are identical")
	}
}

func TestParseErrors(t *testing.T) {
	k := b64Key(t)
	tests := []struct {
		name, env string
	}{
		{"empty", ""},
		{"blank", "  "},
		{"no colon", "m1" + k},
		{"empty id", ":" + k},
		{"bad base64", "m1:!!!"},
		{"short key", "m1:" + base64.StdEncoding.EncodeToString([]byte("short"))},
		{"duplicate id", "m1:" + k + ",m1:" + b64Key(t)},
		{"trailing comma", "m1:" + k + ","},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse(tt.env); err == nil {
				t.Fatal("Parse succeeded")
			}
		})
	}
}
