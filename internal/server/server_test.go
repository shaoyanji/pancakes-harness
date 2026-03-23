package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pancakes-harness/internal/assembler"
	"pancakes-harness/internal/backend"
	"pancakes-harness/internal/eventlog"
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

func TestMetricsReturnsValidJSON(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	srv := newTestServer(t, mem)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode metrics: %v", err)
	}
	if _, ok := out["requests_total"]; !ok {
		t.Fatalf("expected requests_total in metrics, got %#v", out)
	}
}

func TestMetricsUpdateAfterTurn(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	srv := newTestServer(t, mem)

	turnRec := httptest.NewRecorder()
	turnReq := httptest.NewRequest(http.MethodPost, "/v1/turn", bytes.NewReader([]byte(`{"session_id":"s-metrics","branch_id":"main","text":"hello"}`)))
	srv.Handler().ServeHTTP(turnRec, turnReq)
	if turnRec.Code != http.StatusOK {
		t.Fatalf("turn status: %d body=%s", turnRec.Code, turnRec.Body.String())
	}

	metricsRec := httptest.NewRecorder()
	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	srv.Handler().ServeHTTP(metricsRec, metricsReq)
	if metricsRec.Code != http.StatusOK {
		t.Fatalf("metrics status: %d body=%s", metricsRec.Code, metricsRec.Body.String())
	}

	var out struct {
		RequestsTotal map[string]int64 `json:"requests_total"`
		LatenciesMS   map[string]struct {
			Count int64 `json:"count"`
		} `json:"latencies_ms"`
	}
	if err := json.Unmarshal(metricsRec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode metrics: %v", err)
	}
	if out.RequestsTotal["/v1/turn"] < 1 {
		t.Fatalf("expected /v1/turn request counter increment, got %#v", out.RequestsTotal)
	}
	if out.LatenciesMS["turn_latency_ms"].Count < 1 {
		t.Fatalf("expected turn latency observations, got %#v", out.LatenciesMS)
	}
}

func TestMetricsPacketRejectionCounterIncrements(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	a := model.MockAdapter{
		NameValue: "mock",
		CallFunc: func(ctx context.Context, req model.Request) ([]byte, error) {
			return []byte(`{"decision":"answer","answer":"ok"}`), nil
		},
	}
	hugeHeader := strings.Repeat("x", 15000)
	srv, err := New(Config{
		Backend:      mem,
		ModelAdapter: a,
		ToolRunner:   tools.NewRunner(nil),
		ModelHeaders: []assembler.Header{
			{Name: "Content-Type", Value: "application/json"},
			{Name: "X-Huge", Value: hugeHeader},
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}

	turnRec := httptest.NewRecorder()
	turnReq := httptest.NewRequest(http.MethodPost, "/v1/turn", bytes.NewReader([]byte(`{"session_id":"s-reject","branch_id":"main","text":"hello"}`)))
	srv.Handler().ServeHTTP(turnRec, turnReq)
	if turnRec.Code != http.StatusInternalServerError {
		t.Fatalf("expected rejection path, got %d body=%s", turnRec.Code, turnRec.Body.String())
	}

	metricsRec := httptest.NewRecorder()
	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	srv.Handler().ServeHTTP(metricsRec, metricsReq)
	if metricsRec.Code != http.StatusOK {
		t.Fatalf("metrics status: %d body=%s", metricsRec.Code, metricsRec.Body.String())
	}
	var out struct {
		PacketBudgetRejectionsTotal int64 `json:"packet_budget_rejections_total"`
	}
	if err := json.Unmarshal(metricsRec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode metrics: %v", err)
	}
	if out.PacketBudgetRejectionsTotal < 1 {
		t.Fatalf("expected packet budget rejection counter to increment, got %d", out.PacketBudgetRejectionsTotal)
	}
}

func TestPostAgentCallSuccessReturnsValidJSON(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	srv := newTestServer(t, mem)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-call", bytes.NewReader([]byte(`{
		"session_id":"s-agent",
		"branch_id":"main",
		"task":"Summarize latest result in one sentence",
		"refs":["branch:head","tool:last"],
		"constraints":{"reply_style":"brief","max_sentences":1},
		"allow_tools":false
	}`)))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		SessionID string `json:"session_id"`
		BranchID  string `json:"branch_id"`
		Decision  string `json:"decision"`
		Answer    string `json:"answer"`
		Trace     struct {
			PacketEventID   string `json:"packet_event_id"`
			ResponseEventID string `json:"response_event_id"`
		} `json:"trace"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.SessionID != "s-agent" || out.BranchID != "main" || out.Decision != "answer" || out.Answer == "" {
		t.Fatalf("unexpected payload: %#v", out)
	}
	if out.Trace.PacketEventID == "" || out.Trace.ResponseEventID == "" {
		t.Fatalf("expected trace ids, got %#v", out.Trace)
	}
}

func TestPostAgentCallMalformedRequestReturnsCleanJSON(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	srv := newTestServer(t, mem)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-call", bytes.NewReader([]byte(`{"session_id":"s1","task":12,"allow_tools":false}`)))
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
		t.Fatalf("unexpected error payload: %#v", out)
	}
}

func TestPostAgentCallUnknownRefsDoNotCrash(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	srv := newTestServer(t, mem)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-call", bytes.NewReader([]byte(`{
		"session_id":"s-refs",
		"branch_id":"main",
		"task":"answer normally",
		"refs":["branch:does-not-exist","tool:unknown"],
		"allow_tools":false
	}`)))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPostAgentCallAllowToolsFalsePreventsToolExecution(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	toolModel := model.MockAdapter{
		NameValue: "mock-tools",
		CallFunc: func(ctx context.Context, req model.Request) ([]byte, error) {
			return []byte(`{"decision":"tool_calls","tool_calls":[{"tool":"echo_tool","call_id":"c1","args":{"q":"x"}}]}`), nil
		},
	}
	runner := tools.NewRunner(map[string]tools.CommandSpec{
		"echo_tool": {
			Path: "sh",
			Args: []string{"-c", `cat >/dev/null; printf '{"ok":true,"call_id":"c1","result":{"payload":"ran"},"summary":"ran","artifacts":[]}'`},
		},
	})
	srv := newCustomTestServer(t, mem, toolModel, runner)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-call", bytes.NewReader([]byte(`{
		"session_id":"s-no-tools",
		"branch_id":"main",
		"task":"do thing",
		"allow_tools":false
	}`)))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Error.Code != "tools_disabled" {
		t.Fatalf("unexpected error code: %#v", out)
	}
	events, err := mem.ListEventsBySession(context.Background(), "s-no-tools")
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	for _, e := range events {
		if e.Kind == eventlog.KindToolRequest || e.Kind == eventlog.KindToolResult || e.Kind == eventlog.KindToolFailure {
			t.Fatalf("tool events should not exist when allow_tools=false, found %q", e.Kind)
		}
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
	srv := newCustomTestServer(t, b, a, tools.NewRunner(nil))
	return srv
}

func newCustomTestServer(t *testing.T, b backend.Backend, a model.Adapter, runner *tools.Runner) *Server {
	t.Helper()
	srv, err := New(Config{
		Backend:      b,
		ModelAdapter: a,
		ToolRunner:   runner,
		ModelHeaders: []assembler.Header{{Name: "Content-Type", Value: "application/json"}},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return srv
}
