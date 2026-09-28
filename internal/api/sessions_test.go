package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
)

type fakeRepo struct {
	createSeq, postSeq, lastSeq int64
	createErr, postErr          error
	lastSeqErr, listErr         error
	events                      []eventlog.Event
}

func (f *fakeRepo) CreateSession(context.Context, eventlog.TenantScope, uuid.UUID, uuid.UUID, string) (int64, error) {
	return f.createSeq, f.createErr
}

func (f *fakeRepo) PostMessage(context.Context, eventlog.TenantScope, uuid.UUID, uuid.UUID, string) (int64, error) {
	return f.postSeq, f.postErr
}

func (f *fakeRepo) SessionLastSeq(context.Context, eventlog.TenantScope, uuid.UUID) (int64, error) {
	return f.lastSeq, f.lastSeqErr
}

func (f *fakeRepo) ListEvents(context.Context, eventlog.TenantScope, uuid.UUID, int64) ([]eventlog.Event, error) {
	return f.events, f.listErr
}

func TestCreateSessionHandler(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		repo       *fakeRepo
		wantStatus int
	}{
		{
			name:       "valid request",
			body:       `{"session_id":"` + uuid.New().String() + `","client_msg_id":"` + uuid.New().String() + `","message":"hi"}`,
			repo:       &fakeRepo{createSeq: 2},
			wantStatus: http.StatusOK,
		},
		{
			name:       "missing message",
			body:       `{"session_id":"` + uuid.New().String() + `","client_msg_id":"` + uuid.New().String() + `","message":""}`,
			repo:       &fakeRepo{},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid json",
			body:       `not json`,
			repo:       &fakeRepo{},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(tt.repo)
			req := httptest.NewRequest(http.MethodPost, "/api/sessions", bytes.NewBufferString(tt.body))
			rr := httptest.NewRecorder()
			srv.Handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rr.Code, tt.wantStatus, rr.Body.String())
			}
			if tt.wantStatus == http.StatusOK {
				var got struct {
					LastSeq int64 `json:"last_seq"`
				}
				if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
					t.Fatalf("decode body: %v", err)
				}
				if got.LastSeq != tt.repo.createSeq {
					t.Errorf("last_seq = %d, want %d", got.LastSeq, tt.repo.createSeq)
				}
			}
		})
	}
}

func TestPostMessageHandler(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		repo       *fakeRepo
		wantStatus int
	}{
		{
			name:       "valid request",
			body:       `{"client_msg_id":"` + uuid.New().String() + `","message":"hi"}`,
			repo:       &fakeRepo{postSeq: 3},
			wantStatus: http.StatusOK,
		},
		{
			name:       "session not found",
			body:       `{"client_msg_id":"` + uuid.New().String() + `","message":"hi"}`,
			repo:       &fakeRepo{postErr: eventlog.ErrNotFound},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "missing client_msg_id",
			body:       `{"message":"hi"}`,
			repo:       &fakeRepo{},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestServer(tt.repo)
			path := "/api/sessions/" + uuid.New().String() + "/messages"
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(tt.body))
			rr := httptest.NewRecorder()
			srv.Handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rr.Code, tt.wantStatus, rr.Body.String())
			}
		})
	}
}

func TestSessionEventsHandler(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		srv := newTestServer(&fakeRepo{lastSeqErr: eventlog.ErrNotFound})
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+uuid.New().String()+"/events", nil)
		rr := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
		}
	})

	t.Run("replays events then closes on disconnect", func(t *testing.T) {
		repo := &fakeRepo{
			lastSeq: 2,
			events: []eventlog.Event{
				{Seq: 1, Type: eventlog.TypeSessionCreated, CreatedAt: time.Now(), Payload: []byte(`{"a":1}`)},
				{Seq: 2, Type: eventlog.TypeUserMessage, CreatedAt: time.Now(), Payload: []byte(`{"b":2}`)},
			},
		}
		srv := newTestServer(repo)

		ctx, cancel := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodGet, "/api/sessions/"+uuid.New().String()+"/events", nil).WithContext(ctx)
		rr := httptest.NewRecorder()

		done := make(chan struct{})
		go func() {
			srv.Handler.ServeHTTP(rr, req)
			close(done)
		}()

		time.Sleep(50 * time.Millisecond)
		cancel()

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("handler did not exit after client disconnect")
		}

		if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream" {
			t.Errorf("Content-Type = %q, want text/event-stream", ct)
		}
		body := rr.Body.String()
		if !strings.Contains(body, "id: 1\n") {
			t.Errorf("body missing seq 1 frame: %q", body)
		}
		if !strings.Contains(body, "id: 2\n") {
			t.Errorf("body missing seq 2 frame: %q", body)
		}
	})
}

func TestParseAfter(t *testing.T) {
	tests := []struct {
		name        string
		lastEventID string
		after       string
		lastSeq     int64
		want        int64
	}{
		{"no header or query replays from 0", "", "", 10, 0},
		{"header wins over query", "5", "1", 10, 5},
		{"query used when no header", "", "7", 10, 7},
		{"non-numeric header replays from 0", "abc", "", 10, 0},
		{"header past last_seq replays from 0", "20", "", 10, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/sessions/x/events?after="+tt.after, nil)
			if tt.lastEventID != "" {
				req.Header.Set("Last-Event-ID", tt.lastEventID)
			}
			got := parseAfter(req, tt.lastSeq)
			if got != tt.want {
				t.Errorf("parseAfter() = %d, want %d", got, tt.want)
			}
		})
	}
}
