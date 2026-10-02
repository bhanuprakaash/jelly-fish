package eventlog_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/testdb"
)

const testType = "test.renamed"

func addField(key string, val any) eventlog.Upcaster {
	return func(payload []byte) ([]byte, error) {
		var m map[string]any
		if err := json.Unmarshal(payload, &m); err != nil {
			return nil, err
		}
		m[key] = val
		return json.Marshal(m)
	}
}

func TestUpcast(t *testing.T) {
	v1v2 := eventlog.Upcasters{testType: {addField("b", 2)}}
	v1v3 := eventlog.Upcasters{testType: {addField("b", 2), addField("c", 3)}}
	failing := eventlog.Upcasters{testType: {func([]byte) ([]byte, error) { return nil, errors.New("boom") }}}

	tests := []struct {
		name    string
		up      eventlog.Upcasters
		typ     string
		version int
		in      string
		want    string
		wantErr error
	}{
		{"v1 to latest", v1v2, testType, 1, `{"a":1}`, `{"a":1,"b":2}`, nil},
		{"latest is untouched", v1v2, testType, 2, `{"a":1}`, `{"a":1}`, nil},
		{"chain of two", v1v3, testType, 1, `{"a":1}`, `{"a":1,"b":2,"c":3}`, nil},
		{"starts mid-chain", v1v3, testType, 2, `{"a":1}`, `{"a":1,"c":3}`, nil},
		{"unregistered type is v1", v1v2, "other", 1, `{"a":1}`, `{"a":1}`, nil},
		{"zero value knows v1 only", nil, testType, 1, `{"a":1}`, `{"a":1}`, nil},
		{"newer than latest", v1v2, testType, 3, `{"a":1}`, "", eventlog.ErrSchemaTooNew},
		{"newer than an unregistered type", v1v2, "other", 2, `{"a":1}`, "", eventlog.ErrSchemaTooNew},
		{"zero value rejects v2", nil, testType, 2, `{"a":1}`, "", eventlog.ErrSchemaTooNew},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.up.Upcast(tc.typ, tc.version, []byte(tc.in))
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, []byte(tc.want)) {
				t.Fatalf("payload = %s, want %s", got, tc.want)
			}
		})
	}

	if _, err := failing.Upcast(testType, 1, []byte(`{}`)); err == nil || errors.Is(err, eventlog.ErrSchemaTooNew) {
		t.Fatalf("a failing upcaster: err = %v, want a plain error", err)
	}
}

func TestUpcastersEvents(t *testing.T) {
	up := eventlog.Upcasters{testType: {addField("b", 2)}}
	in := []eventlog.Event{
		{Seq: 1, Type: testType, SchemaVersion: 1, Payload: []byte(`{"a":1}`)},
		{Seq: 2, Type: "other", SchemaVersion: 1, Payload: []byte(`{"a":1}`)},
	}
	got, err := up.Events(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(got[0].Payload) != `{"a":1,"b":2}` || got[0].SchemaVersion != 2 {
		t.Fatalf("event 0 = %s v%d, want upcast to v2", got[0].Payload, got[0].SchemaVersion)
	}
	if string(got[1].Payload) != `{"a":1}` || got[1].SchemaVersion != 1 {
		t.Fatalf("event 1 = %s v%d, want unchanged v1", got[1].Payload, got[1].SchemaVersion)
	}
	if string(in[0].Payload) != `{"a":1}` || in[0].SchemaVersion != 1 {
		t.Fatalf("input mutated: %s v%d", in[0].Payload, in[0].SchemaVersion)
	}

	in[1].SchemaVersion = 2
	if _, err := up.Events(in); !errors.Is(err, eventlog.ErrSchemaTooNew) {
		t.Fatalf("err = %v, want ErrSchemaTooNew", err)
	}
}

func TestLoadUpcastsWithoutRewritingStoredRows(t *testing.T) {
	pool := testdb.NewPool(t)
	store := eventlog.NewStore(pool)
	sid := uuid.New()
	if _, err := eventlog.NewRepo(pool, "fake").CreateSession(t.Context(), testdb.NewUser(t, pool).Scope(), sid, uuid.New(), "hi", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(t.Context(), sid, nil, []eventlog.NewEvent{{Type: testType, Actor: "test", Payload: map[string]int{"a": 1}}}, nil); err != nil {
		t.Fatal(err)
	}

	snapshot := func() string {
		t.Helper()
		var s string
		err := pool.QueryRow(t.Context(), `
			SELECT string_agg(seq || ':' || schema_version || ':' || payload::text, '|' ORDER BY seq)
			FROM events WHERE session_id = $1`, sid).Scan(&s)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := snapshot()

	evs, err := store.Load(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	up, err := eventlog.Upcasters{testType: {addField("b", 2)}}.Events(evs)
	if err != nil {
		t.Fatal(err)
	}

	last := up[len(up)-1]
	if last.Type != testType || last.SchemaVersion != 2 {
		t.Fatalf("last event = %s v%d, want %s v2", last.Type, last.SchemaVersion, testType)
	}
	var p map[string]int
	if err := json.Unmarshal(last.Payload, &p); err != nil {
		t.Fatal(err)
	}
	if p["a"] != 1 || p["b"] != 2 {
		t.Fatalf("upcast payload = %v, want a and b", p)
	}
	if evs[len(evs)-1].SchemaVersion != 1 {
		t.Fatalf("loaded event SchemaVersion = %d, want the stored 1", evs[len(evs)-1].SchemaVersion)
	}
	if after := snapshot(); after != before {
		t.Fatalf("stored rows changed by a read:\nbefore %s\nafter  %s", before, after)
	}
}
