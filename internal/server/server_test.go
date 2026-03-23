package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"pancakes-harness/internal/assembler"
	"pancakes-harness/internal/backend"
	"pancakes-harness/internal/model"
	"pancakes-harness/internal/tools"
)

func TestPostTurnReturnsValidAnswerResponse(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	srv := newTestServer(t, mem)
	reqBody := []byte(`{"session_id":"s-turn","branch_id":"main","text":"hello harness"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/turn", bytes.NewReader(reqBody))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		SessionID     string `json:"session_id"`
		BranchID      string `json:"branch_id"`
		Answer        string `json:"answer"`
		Decision      string `json:"decision"`
		EnvelopeBytes int    `json:"envelope_bytes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Answer == "" || out.Decision != "answer" {
		t.Fatalf("unexpected response: %#v", out)
	}
	if out.EnvelopeBytes <= 0 {
		t.Fatalf("expected positive envelope_bytes, got %d", out.EnvelopeBytes)
	}
}

func TestPostTurnMalformedInputReturnsCleanJSON(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	srv := newTestServer(t, mem)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/turn", bytes.NewReader([]byte(`{"session_id":"s1","text":123}`)))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Error.Code != "bad_request" || out.Error.Message == "" {
		t.Fatalf("unexpected error response: %#v", out)
	}
}

func TestPostBranchForkWorksAndDoesNotCopyTranscript(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	srv := newTestServer(t, mem)

	turnReq := httptest.NewRequest(http.MethodPost, "/v1/turn", bytes.NewReader([]byte(`{"session_id":"s-fork","branch_id":"main","text":"one"}`)))
	turnRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(turnRec, turnReq)
	if turnRec.Code != http.StatusOK {
		t.Fatalf("turn status: %d body=%s", turnRec.Code, turnRec.Body.String())
	}

	forkReq := httptest.NewRequest(http.MethodPost, "/v1/branch/fork", bytes.NewReader([]byte(`{"session_id":"s-fork","parent_branch_id":"main","child_branch_id":"alt-1"}`)))
	forkRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(forkRec, forkReq)
	if forkRec.Code != http.StatusOK {
		t.Fatalf("fork status: %d body=%s", forkRec.Code, forkRec.Body.String())
	}

	mainEvents, err := mem.ListEventsByBranch(context.Background(), "s-fork", "main")
	if err != nil {
		t.Fatalf("list main branch: %v", err)
	}
	childEvents, err := mem.ListEventsByBranch(context.Background(), "s-fork", "alt-1")
	if err != nil {
		t.Fatalf("list child branch: %v", err)
	}
	if len(mainEvents) == 0 || len(childEvents) != 1 {
		t.Fatalf("unexpected branch event counts: main=%d child=%d", len(mainEvents), len(childEvents))
	}
	if childEvents[0].Kind != "branch.fork" {
		t.Fatalf("expected child branch to contain only fork event, got %q", childEvents[0].Kind)
	}
}

func TestGetSessionReplayReturnsReplayData(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	srv := newTestServer(t, mem)

	turnReq := httptest.NewRequest(http.MethodPost, "/v1/turn", bytes.NewReader([]byte(`{"session_id":"s-replay","branch_id":"main","text":"hi"}`)))
	turnRec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(turnRec, turnReq)
	if turnRec.Code != http.StatusOK {
		t.Fatalf("turn status: %d body=%s", turnRec.Code, turnRec.Body.String())
	}

	replayRec := httptest.NewRecorder()
	replayReq := httptest.NewRequest(http.MethodGet, "/v1/session/s-replay/replay", nil)
	srv.Handler().ServeHTTP(replayRec, replayReq)
	if replayRec.Code != http.StatusOK {
		t.Fatalf("replay status: %d body=%s", replayRec.Code, replayRec.Body.String())
	}
	var out struct {
		SessionID string            `json:"session_id"`
		Branches  map[string]string `json:"branches"`
		State     struct {
			EventCount int `json:"event_count"`
		} `json:"state"`
	}
	if err := json.Unmarshal(replayRec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode replay: %v", err)
	}
	if out.SessionID != "s-replay" || out.State.EventCount == 0 || out.Branches["main"] == "" {
		t.Fatalf("unexpected replay payload: %#v", out)
	}
}

func TestGetHealthzReflectsBackendHealth(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	srv := newTestServer(t, mem)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		OK      bool `json:"ok"`
		Backend struct {
			OK bool `json:"ok"`
		} `json:"backend"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK || !out.Backend.OK {
		t.Fatalf("expected healthy backend response, got %#v", out)
	}
}

func newTestServer(t *testing.T, b backend.Backend) *Server {
	t.Helper()
	a := model.MockAdapter{
		NameValue: "mock-http-api",
		CallFunc: func(ctx context.Context, req model.Request) ([]byte, error) {
			return []byte(`{"decision":"answer","answer":"hello from server"}`), nil
		},
	}
	srv, err := New(Config{
		Backend:      b,
		ModelAdapter: a,
		ToolRunner:   tools.NewRunner(nil),
		ModelHeaders: []assembler.Header{{Name: "Content-Type", Value: "application/json"}},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return srv
}
