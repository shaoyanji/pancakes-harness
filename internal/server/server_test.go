package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
		SessionID   string `json:"session_id"`
		BranchID    string `json:"branch_id"`
		Decision    string `json:"decision"`
		Answer      string `json:"answer"`
		Resolved    bool   `json:"resolved"`
		Fingerprint string `json:"fingerprint"`
		Trace       struct {
			PacketEventID   string `json:"packet_event_id"`
			ResponseEventID string `json:"response_event_id"`
			ConsultManifest struct {
				SessionID         string            `json:"session_id"`
				BranchID          string            `json:"branch_id"`
				Fingerprint       string            `json:"fingerprint"`
				Refs              []string          `json:"refs"`
				Constraints       map[string]string `json:"constraints"`
				ByteBudget        int               `json:"byte_budget"`
				ActualBytes       int               `json:"actual_bytes"`
				SerializerVersion string            `json:"serializer_version"`
			} `json:"consult_manifest"`
		} `json:"trace"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.SessionID != "s-agent" || out.BranchID != "main" || out.Decision != "answer" || out.Answer == "" {
		t.Fatalf("unexpected payload: %#v", out)
	}
	if !out.Resolved || out.Fingerprint == "" {
		t.Fatalf("expected resolved response with fingerprint, got %#v", out)
	}
	if out.Trace.PacketEventID == "" || out.Trace.ResponseEventID == "" {
		t.Fatalf("expected trace ids, got %#v", out.Trace)
	}
	if out.Trace.ConsultManifest.SessionID != out.SessionID || out.Trace.ConsultManifest.BranchID != out.BranchID {
		t.Fatalf("consult manifest identity mismatch: %#v", out.Trace.ConsultManifest)
	}
	if out.Trace.ConsultManifest.Fingerprint != out.Fingerprint {
		t.Fatalf("consult fingerprint mismatch: response=%q manifest=%q", out.Fingerprint, out.Trace.ConsultManifest.Fingerprint)
	}
	if out.Trace.ConsultManifest.ActualBytes <= 0 || out.Trace.ConsultManifest.ByteBudget <= 0 {
		t.Fatalf("consult bytes not populated: %#v", out.Trace.ConsultManifest)
	}
	if out.Trace.ConsultManifest.SerializerVersion == "" {
		t.Fatalf("missing consult serializer version: %#v", out.Trace.ConsultManifest)
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

func TestPostAgentCallMalformedBoundaryInputReturnsStructuredError(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	srv := newTestServer(t, mem)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-call", bytes.NewReader([]byte(`{
		"session_id":"s-malformed-boundary",
		"branch_id":"main",
		"task":"do thing",
		"refs":["ok","   "],
		"allow_tools":false
	}`)))
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
	if out.Error.Code != "malformed_boundary_input" || out.Error.Message == "" {
		t.Fatalf("unexpected error payload: %#v", out)
	}
}

func TestPostAgentCallUnresolvedDoesNotExecute(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	var calls int32
	a := model.MockAdapter{
		NameValue: "mock-unresolved",
		CallFunc: func(ctx context.Context, req model.Request) ([]byte, error) {
			atomic.AddInt32(&calls, 1)
			return []byte(`{"decision":"answer","answer":"should not execute"}`), nil
		},
	}
	srv := newCustomTestServer(t, mem, a, tools.NewRunner(nil))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-call", bytes.NewReader([]byte(`{
		"session_id":"s-unresolved",
		"task":"do thing",
		"allow_tools":false
	}`)))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Decision string   `json:"decision"`
		Resolved bool     `json:"resolved"`
		Missing  []string `json:"missing"`
		Trace    struct {
			ConsultManifest any `json:"consult_manifest"`
		} `json:"trace"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Decision != "unresolved" || out.Resolved {
		t.Fatalf("unexpected unresolved payload: %#v", out)
	}
	if len(out.Missing) != 1 || out.Missing[0] != "scope" {
		t.Fatalf("expected missing scope, got %#v", out.Missing)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("expected no model execution, got %d calls", calls)
	}
	if out.Trace.ConsultManifest != nil {
		t.Fatalf("unresolved response must not fabricate consult manifest: %#v", out.Trace)
	}
}

func TestPostAgentCallResolvedFingerprintUsesNormalizedIntent(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	srv := newTestServer(t, mem)

	call := func(body string) (string, string, int) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/agent-call", bytes.NewReader([]byte(body)))
		srv.Handler().ServeHTTP(rec, req)
		var out struct {
			Fingerprint string `json:"fingerprint"`
			Trace       struct {
				ConsultManifest json.RawMessage `json:"consult_manifest"`
			} `json:"trace"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out.Fingerprint, string(out.Trace.ConsultManifest), rec.Code
	}

	fp1, manifest1, code1 := call(`{
		"session_id":"s-normalize",
		"branch_id":"main",
		"task":"  summarize this  ",
		"refs":["tool:last","branch:head"],
		"constraints":{"max_sentences":1,"reply_style":"brief"},
		"allow_tools":false
	}`)
	fp2, manifest2, code2 := call(`{
		"session_id":"s-normalize",
		"branch_id":"main",
		"task":"summarize this",
		"refs":["branch:head","tool:last"],
		"constraints":{"reply_style":"brief","max_sentences":1},
		"allow_tools":false
	}`)

	if code1 != http.StatusOK || code2 != http.StatusOK {
		t.Fatalf("unexpected statuses: code1=%d code2=%d", code1, code2)
	}
	if fp1 == "" || fp2 == "" {
		t.Fatalf("expected non-empty fingerprints, got %q and %q", fp1, fp2)
	}
	if fp1 != fp2 {
		t.Fatalf("expected equal normalized fingerprints, got %q vs %q", fp1, fp2)
	}
	if manifest1 == "" || manifest2 == "" {
		t.Fatalf("expected consult manifests for resolved requests, got %q and %q", manifest1, manifest2)
	}
	if manifest1 != manifest2 {
		t.Fatalf("expected identical manifests for equivalent normalized requests, got %s vs %s", manifest1, manifest2)
	}
}

func TestPostAgentCallExternalContextAffectsFingerprintAndNormalizesWhitespace(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	srv := newTestServer(t, mem)

	call := func(body string) (string, int) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/agent-call", bytes.NewReader([]byte(body)))
		srv.Handler().ServeHTTP(rec, req)
		var out struct {
			Fingerprint string `json:"fingerprint"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out.Fingerprint, rec.Code
	}

	base := `{
		"session_id":"s-ext-fp",
		"branch_id":"main",
		"task":"summarize this",
		"allow_tools":false
	}`
	withExternal := `{
		"session_id":"s-ext-fp",
		"branch_id":"main",
		"task":"summarize this",
		"external_context":"AURA FIXED CONTEXT BLOCK",
		"allow_tools":false
	}`
	withWhitespace := `{
		"session_id":"s-ext-fp",
		"branch_id":"main",
		"task":"summarize this",
		"external_context":"   \n\t  ",
		"allow_tools":false
	}`

	baseFP, baseCode := call(base)
	extFP, extCode := call(withExternal)
	whiteFP, whiteCode := call(withWhitespace)

	if baseCode != http.StatusOK || extCode != http.StatusOK || whiteCode != http.StatusOK {
		t.Fatalf("unexpected statuses: base=%d ext=%d white=%d", baseCode, extCode, whiteCode)
	}
	if baseFP == "" || extFP == "" || whiteFP == "" {
		t.Fatalf("expected non-empty fingerprints, got base=%q ext=%q white=%q", baseFP, extFP, whiteFP)
	}
	if baseFP == extFP {
		t.Fatalf("expected external context to change fingerprint, got %q", baseFP)
	}
	if baseFP != whiteFP {
		t.Fatalf("expected whitespace external context to normalize as omitted, got %q vs %q", baseFP, whiteFP)
	}
}

func TestPostAgentCallCoalescingUsesStabilizedFingerprint(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	var calls int32
	a := model.MockAdapter{
		NameValue: "mock-coalesce",
		CallFunc: func(ctx context.Context, req model.Request) ([]byte, error) {
			atomic.AddInt32(&calls, 1)
			time.Sleep(120 * time.Millisecond)
			return []byte(`{"decision":"answer","answer":"coalesced"}`), nil
		},
	}
	srv := newCustomTestServer(t, mem, a, tools.NewRunner(nil))

	bodyA := `{
		"session_id":"s-coalesce",
		"branch_id":"main",
		"task":" summarize this ",
		"refs":["r2","r1"],
		"constraints":{"a":1,"b":"x"},
		"allow_tools":false
	}`
	bodyB := `{
		"session_id":"s-coalesce",
		"branch_id":"main",
		"task":"summarize this",
		"refs":["r1","r2"],
		"constraints":{"b":"x","a":1},
		"allow_tools":false
	}`

	type result struct {
		code int
		body string
	}
	results := make(chan result, 2)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/agent-call", bytes.NewReader([]byte(bodyA)))
		srv.Handler().ServeHTTP(rec, req)
		results <- result{code: rec.Code, body: rec.Body.String()}
	}()
	go func() {
		defer wg.Done()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/agent-call", bytes.NewReader([]byte(bodyB)))
		srv.Handler().ServeHTTP(rec, req)
		results <- result{code: rec.Code, body: rec.Body.String()}
	}()
	wg.Wait()
	close(results)

	got := make([]string, 0, 2)
	for res := range results {
		if res.code != http.StatusOK {
			t.Fatalf("unexpected status: %d body=%s", res.code, res.body)
		}
		got = append(got, res.body)
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("expected exactly one model call after coalescing, got %d", calls)
	}
	if len(got) != 2 {
		t.Fatalf("expected two responses, got %d", len(got))
	}
	if got[0] != got[1] {
		t.Fatalf("expected identical coalesced payloads, got\n%s\nvs\n%s", got[0], got[1])
	}

	var out1 struct {
		Trace struct {
			ConsultManifest json.RawMessage `json:"consult_manifest"`
		} `json:"trace"`
	}
	var out2 struct {
		Trace struct {
			ConsultManifest json.RawMessage `json:"consult_manifest"`
		} `json:"trace"`
	}
	if err := json.Unmarshal([]byte(got[0]), &out1); err != nil {
		t.Fatalf("decode coalesced payload 1: %v", err)
	}
	if err := json.Unmarshal([]byte(got[1]), &out2); err != nil {
		t.Fatalf("decode coalesced payload 2: %v", err)
	}
	if len(out1.Trace.ConsultManifest) == 0 {
		t.Fatalf("expected consult manifest on coalesced resolved response: %s", got[0])
	}
	if string(out1.Trace.ConsultManifest) != string(out2.Trace.ConsultManifest) {
		t.Fatalf("expected identical consult manifests for leader/follower, got %s vs %s", string(out1.Trace.ConsultManifest), string(out2.Trace.ConsultManifest))
	}
}

func TestPostAgentCallOverlappingDifferentRequestsDoNotCrossBleed(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	var calls int32
	a := model.MockAdapter{
		NameValue: "mock-overlap",
		CallFunc: func(ctx context.Context, req model.Request) ([]byte, error) {
			atomic.AddInt32(&calls, 1)
			time.Sleep(80 * time.Millisecond)
			packet := string(req.Packet)
			if strings.Contains(packet, "task: task-a") {
				return []byte(`{"decision":"answer","answer":"answer-a"}`), nil
			}
			if strings.Contains(packet, "task: task-b") {
				return []byte(`{"decision":"answer","answer":"answer-b"}`), nil
			}
			return []byte(`{"decision":"answer","answer":"unknown"}`), nil
		},
	}
	srv := newCustomTestServer(t, mem, a, tools.NewRunner(nil))

	bodyA := `{
		"session_id":"s-overlap",
		"branch_id":"main",
		"task":"task-a",
		"refs":["shared"],
		"allow_tools":false
	}`
	bodyB := `{
		"session_id":"s-overlap",
		"branch_id":"main",
		"task":"task-b",
		"refs":["shared"],
		"allow_tools":false
	}`

	type result struct {
		code  int
		body  string
		label string
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/agent-call", bytes.NewReader([]byte(bodyA)))
		srv.Handler().ServeHTTP(rec, req)
		results <- result{code: rec.Code, body: rec.Body.String(), label: "a"}
	}()
	go func() {
		defer wg.Done()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/agent-call", bytes.NewReader([]byte(bodyB)))
		srv.Handler().ServeHTTP(rec, req)
		results <- result{code: rec.Code, body: rec.Body.String(), label: "b"}
	}()
	wg.Wait()
	close(results)

	seen := map[string]string{}
	for res := range results {
		if res.code != http.StatusOK {
			t.Fatalf("unexpected status for %s: %d body=%s", res.label, res.code, res.body)
		}
		var out struct {
			Answer string `json:"answer"`
		}
		if err := json.Unmarshal([]byte(res.body), &out); err != nil {
			t.Fatalf("decode %s: %v", res.label, err)
		}
		seen[res.label] = out.Answer
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected two model calls for distinct normalized requests, got %d", calls)
	}
	if seen["a"] != "answer-a" || seen["b"] != "answer-b" {
		t.Fatalf("cross-bleed detected, answers=%#v", seen)
	}
}

func TestAgentCallPreflightIntegrationDoesNotRegressTurnPath(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	srv := newTestServer(t, mem)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/turn", bytes.NewReader([]byte(`{"session_id":"s-turn-regress","branch_id":"main","text":"hello"}`)))
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAgentCallResponseIncludesContractVersion(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	srv := newTestServer(t, mem)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-call", bytes.NewReader([]byte(`{
		"session_id":"s-contract",
		"branch_id":"main",
		"task":"test task",
		"allow_tools":false
	}`)))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Contract string `json:"contract"`
		Resolved bool   `json:"resolved"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Resolved {
		t.Fatalf("expected resolved response")
	}
	if out.Contract != "agent_call.v1" {
		t.Fatalf("expected contract=agent_call.v1, got %q", out.Contract)
	}
}

func TestAgentCallUnresolvedResponseIncludesContractVersion(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	srv := newTestServer(t, mem)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-call", bytes.NewReader([]byte(`{
		"session_id":"s-contract-unresolved",
		"task":"test task",
		"allow_tools":false
	}`)))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Contract string `json:"contract"`
		Resolved bool   `json:"resolved"`
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Resolved {
		t.Fatalf("expected unresolved response")
	}
	if out.Decision != "unresolved" {
		t.Fatalf("expected decision=unresolved, got %q", out.Decision)
	}
	if out.Contract != "agent_call.v1" {
		t.Fatalf("expected contract=agent_call.v1, got %q", out.Contract)
	}
}

func TestConsultManifestSerializerVersionIsStable(t *testing.T) {
	t.Parallel()

	mem := backend.NewMemoryBackend()
	srv := newTestServer(t, mem)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-call", bytes.NewReader([]byte(`{
		"session_id":"s-serializer",
		"branch_id":"main",
		"task":"test task",
		"refs":["branch:head"],
		"constraints":{"reply_style":"brief"},
		"allow_tools":false
	}`)))
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Trace struct {
			ConsultManifest struct {
				SerializerVersion string `json:"serializer_version"`
			} `json:"consult_manifest"`
		} `json:"trace"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Trace.ConsultManifest.SerializerVersion != "consult_manifest.v1" {
		t.Fatalf("expected serializer_version=consult_manifest.v1, got %q", out.Trace.ConsultManifest.SerializerVersion)
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
