package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"pancakes-harness/internal/assembler"
	"pancakes-harness/internal/backend"
	"pancakes-harness/internal/consult"
	"pancakes-harness/internal/eventlog"
	ingressctrl "pancakes-harness/internal/ingress"
	"pancakes-harness/internal/metrics"
	"pancakes-harness/internal/model"
	"pancakes-harness/internal/preflight"
	"pancakes-harness/internal/runtime"
	"pancakes-harness/internal/tools"
)

var (
	ErrInvalidConfig = errors.New("invalid server config")
)

type Config struct {
	Backend      backend.Backend
	ModelAdapter model.Adapter
	ToolRunner   *tools.Runner
	ModelHeaders []assembler.Header
	Timeout      time.Duration
	Metrics      *metrics.Registry
	BackendMode  string
	ModelMode    string
}

type Server struct {
	cfg Config

	inflight *ingressctrl.Inflight
}

func New(cfg Config) (*Server, error) {
	if cfg.Backend == nil || cfg.ModelAdapter == nil {
		return nil, ErrInvalidConfig
	}
	if cfg.ToolRunner == nil {
		cfg.ToolRunner = tools.NewRunner(nil)
	}
	if cfg.Metrics == nil {
		cfg.Metrics = metrics.NewRegistry()
	}
	cfg.Metrics.SetModes(cfg.BackendMode, cfg.ModelMode)
	return &Server{cfg: cfg, inflight: ingressctrl.NewInflight()}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/metrics", s.handleMetrics)
	mux.HandleFunc("/v1/turn", s.handleTurn)
	mux.HandleFunc("/v1/agent-call", s.handleAgentCall)
	mux.HandleFunc("/v1/branch/fork", s.handleBranchFork)
	mux.HandleFunc("/v1/session/", s.handleSessionReplay)
	return mux
}

type turnRequest struct {
	SessionID string `json:"session_id"`
	BranchID  string `json:"branch_id"`
	Text      string `json:"text"`
}

type turnResponse struct {
	SessionID     string `json:"session_id"`
	BranchID      string `json:"branch_id"`
	Answer        string `json:"answer"`
	Decision      string `json:"decision"`
	EnvelopeBytes int    `json:"envelope_bytes"`
}

type agentCallRequest struct {
	SessionID   string         `json:"session_id"`
	BranchID    string         `json:"branch_id"`
	Task        string         `json:"task"`
	Refs        []string       `json:"refs,omitempty"`
	Constraints map[string]any `json:"constraints,omitempty"`
	AllowTools  bool           `json:"allow_tools"`
}

type agentCallResponse struct {
	SessionID     string           `json:"session_id"`
	BranchID      string           `json:"branch_id"`
	Decision      string           `json:"decision"`
	Resolved      bool             `json:"resolved"`
	Missing       []string         `json:"missing,omitempty"`
	Fingerprint   string           `json:"fingerprint,omitempty"`
	Reason        string           `json:"reason,omitempty"`
	Answer        string           `json:"answer"`
	ToolCalls     []model.ToolCall `json:"tool_calls"`
	EnvelopeBytes int              `json:"envelope_bytes"`
	Trace         agentCallTrace   `json:"trace"`
}

type agentCallTrace struct {
	PacketEventID   string            `json:"packet_event_id,omitempty"`
	ResponseEventID string            `json:"response_event_id,omitempty"`
	ConsultManifest *consult.Manifest `json:"consult_manifest,omitempty"`
}

type coalescedAgentCallResult struct {
	Status       int
	Response     *agentCallResponse
	ErrorCode    string
	ErrorMessage string
}

type branchForkRequest struct {
	SessionID      string `json:"session_id"`
	ParentBranchID string `json:"parent_branch_id"`
	ChildBranchID  string `json:"child_branch_id"`
}

type branchForkResponse struct {
	OK bool `json:"ok"`
}

type replayResponse struct {
	SessionID string              `json:"session_id"`
	Branches  map[string]string   `json:"branches"`
	State     replayStateResponse `json:"state"`
}

type replayStateResponse struct {
	SessionID   string            `json:"session_id"`
	BranchHeads map[string]string `json:"branch_heads"`
	LastEventID string            `json:"last_event_id"`
	EventCount  int               `json:"event_count"`
}

type healthzResponse struct {
	OK      bool                 `json:"ok"`
	Backend backendHealthPayload `json:"backend"`
}

type backendHealthPayload struct {
	OK          bool                `json:"ok"`
	Diagnostics []backendDiagnostic `json:"diagnostics"`
}

type backendDiagnostic struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details map[string]string `json:"details,omitempty"`
}

type errorResponse struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (s *Server) handleTurn(w http.ResponseWriter, r *http.Request) {
	const route = "/v1/turn"
	s.cfg.Metrics.IncRequest(route)
	started := time.Now()
	defer s.cfg.Metrics.ObserveLatency("turn_latency_ms", time.Since(started))

	if r.Method != http.MethodPost {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	var req turnRequest
	if err := decodeJSONBody(r, &req); err != nil {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.BranchID = strings.TrimSpace(req.BranchID)
	req.Text = strings.TrimSpace(req.Text)
	if req.SessionID == "" || req.Text == "" {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusBadRequest, "bad_request", "session_id and text are required")
		return
	}
	branch := req.BranchID
	if branch == "" {
		branch = "main"
	}

	session, err := s.startSession(req.SessionID, branch)
	if err != nil {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusInternalServerError, "runtime_error", err.Error())
		return
	}
	ctx, cancel := s.requestContext(r.Context())
	defer cancel()
	res, err := session.HandleUserTurn(ctx, branch, req.Text)
	if err != nil {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusInternalServerError, "turn_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, turnResponse{
		SessionID:     res.SessionID,
		BranchID:      res.BranchID,
		Answer:        res.Answer,
		Decision:      res.Decision,
		EnvelopeBytes: res.PacketEnvelopeBytes,
	})
}

func (s *Server) handleAgentCall(w http.ResponseWriter, r *http.Request) {
	const route = "/v1/agent-call"
	s.cfg.Metrics.IncRequest(route)
	started := time.Now()
	defer s.cfg.Metrics.ObserveLatency("agent_call_latency_ms", time.Since(started))

	if r.Method != http.MethodPost {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	var req agentCallRequest
	if err := decodeJSONBody(r, &req); err != nil {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.BranchID = strings.TrimSpace(req.BranchID)
	req.Task = strings.TrimSpace(req.Task)
	if req.SessionID == "" {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusBadRequest, "bad_request", "session_id is required")
		return
	}
	if req.Task == "" {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusBadRequest, "malformed_boundary_input", "task is required")
		return
	}

	normalizedConstraints, err := normalizeAgentConstraints(req.Constraints)
	if err != nil {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusBadRequest, "malformed_boundary_input", err.Error())
		return
	}
	pfResult, err := preflight.Validate(preflight.Input{
		Mode:           "agent_call",
		Scope:          req.BranchID,
		AllowExecution: true,
		AllowTools:     req.AllowTools,
		Refs:           req.Refs,
		Constraints:    normalizedConstraints,
		Reason:         req.Task,
	})
	if err != nil {
		s.cfg.Metrics.IncError(route)
		if errors.Is(err, preflight.ErrMalformedInput) {
			writeJSONError(w, http.StatusBadRequest, "malformed_boundary_input", pfResult.Reason)
			return
		}
		writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if !pfResult.Resolved {
		writeJSON(w, http.StatusOK, agentCallResponse{
			SessionID: req.SessionID,
			BranchID:  req.BranchID,
			Decision:  "unresolved",
			Resolved:  false,
			Missing:   pfResult.Missing,
			Reason:    pfResult.Reason,
			ToolCalls: []model.ToolCall{},
		})
		return
	}
	branch := pfResult.Scope

	task := req.Task
	text, err := buildAgentCallText(task, pfResult.Refs, pfResult.Constraints)
	if err != nil {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusBadRequest, "malformed_boundary_input", err.Error())
		return
	}

	fingerprint, err := ingressctrl.Fingerprint(ingressctrl.FingerprintInput{
		SessionID:   req.SessionID,
		BranchID:    branch,
		Task:        task,
		Refs:        pfResult.Refs,
		Constraints: pfResult.Constraints,
		AllowTools:  pfResult.AllowTools,
	})
	if err != nil {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusBadRequest, "malformed_boundary_input", "unable to fingerprint request")
		return
	}
	manifest, err := buildConsultManifest(req.SessionID, branch, fingerprint, pfResult, task)
	if err != nil {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusInternalServerError, "consult_manifest_failed", err.Error())
		return
	}

	ticket := s.inflight.Enter(fingerprint)
	if !ticket.Leader() {
		ctx, cancel := s.requestContext(r.Context())
		defer cancel()
		val, err := ticket.WaitValue(ctx)
		if err != nil {
			s.cfg.Metrics.IncError(route)
			writeJSONError(w, http.StatusGatewayTimeout, "inflight_wait_failed", err.Error())
			return
		}
		out, ok := val.(coalescedAgentCallResult)
		if !ok {
			s.cfg.Metrics.IncError(route)
			writeJSONError(w, http.StatusInternalServerError, "coalesced_result_missing", "missing coalesced leader result")
			return
		}
		writeCoalescedAgentCallResult(w, out)
		return
	}
	publish := func(out coalescedAgentCallResult) {
		ticket.Complete(out, nil)
	}

	toolRunner := s.cfg.ToolRunner
	if !pfResult.AllowTools {
		toolRunner = nil
	}
	session, err := s.startSessionWithRunner(req.SessionID, branch, toolRunner)
	if err != nil {
		s.cfg.Metrics.IncError(route)
		out := coalescedAgentCallResult{
			Status:       http.StatusInternalServerError,
			ErrorCode:    "runtime_error",
			ErrorMessage: err.Error(),
		}
		publish(out)
		writeCoalescedAgentCallResult(w, out)
		return
	}

	ctx, cancel := s.requestContext(r.Context())
	defer cancel()
	res, err := session.HandleUserTurn(ctx, branch, text)
	if err != nil {
		if errors.Is(err, runtime.ErrNoToolRunnerConfigured) && !pfResult.AllowTools {
			s.cfg.Metrics.IncError(route)
			out := coalescedAgentCallResult{
				Status:       http.StatusUnprocessableEntity,
				ErrorCode:    "tools_disabled",
				ErrorMessage: "model requested tool execution while allow_tools=false",
			}
			publish(out)
			writeCoalescedAgentCallResult(w, out)
			return
		}
		s.cfg.Metrics.IncError(route)
		out := coalescedAgentCallResult{
			Status:       http.StatusInternalServerError,
			ErrorCode:    "agent_call_failed",
			ErrorMessage: err.Error(),
		}
		publish(out)
		writeCoalescedAgentCallResult(w, out)
		return
	}

	trace := s.findTrace(req.SessionID, branch)
	trace.ConsultManifest = &manifest
	resp := agentCallResponse{
		SessionID:     res.SessionID,
		BranchID:      res.BranchID,
		Decision:      res.Decision,
		Resolved:      true,
		Fingerprint:   fingerprint,
		Reason:        pfResult.Reason,
		Answer:        res.Answer,
		ToolCalls:     []model.ToolCall{},
		EnvelopeBytes: res.PacketEnvelopeBytes,
		Trace:         trace,
	}
	out := coalescedAgentCallResult{
		Status:   http.StatusOK,
		Response: &resp,
	}
	publish(out)
	writeCoalescedAgentCallResult(w, out)
}

func (s *Server) handleBranchFork(w http.ResponseWriter, r *http.Request) {
	const route = "/v1/branch/fork"
	s.cfg.Metrics.IncRequest(route)
	if r.Method != http.MethodPost {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	var req branchForkRequest
	if err := decodeJSONBody(r, &req); err != nil {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.ParentBranchID = strings.TrimSpace(req.ParentBranchID)
	req.ChildBranchID = strings.TrimSpace(req.ChildBranchID)
	if req.SessionID == "" || req.ParentBranchID == "" || req.ChildBranchID == "" {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusBadRequest, "bad_request", "session_id, parent_branch_id, and child_branch_id are required")
		return
	}

	session, err := s.startSession(req.SessionID, req.ParentBranchID)
	if err != nil {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusInternalServerError, "runtime_error", err.Error())
		return
	}
	ctx, cancel := s.requestContext(r.Context())
	defer cancel()
	if err := session.ForkBranch(ctx, req.ParentBranchID, req.ChildBranchID); err != nil {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusInternalServerError, "fork_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, branchForkResponse{OK: true})
}

func (s *Server) handleSessionReplay(w http.ResponseWriter, r *http.Request) {
	const route = "/v1/session/{id}/replay"
	s.cfg.Metrics.IncRequest(route)
	started := time.Now()
	defer s.cfg.Metrics.ObserveLatency("replay_latency_ms", time.Since(started))

	if r.Method != http.MethodGet {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	sessionID, ok := parseReplayPath(r.URL.Path)
	if !ok {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}

	session, err := s.startSession(sessionID, "main")
	if err != nil {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusInternalServerError, "runtime_error", err.Error())
		return
	}
	ctx, cancel := s.requestContext(r.Context())
	defer cancel()
	replayed, err := session.ReplaySession(ctx)
	if err != nil {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusInternalServerError, "replay_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, replayResponse{
		SessionID: sessionID,
		Branches:  replayed.Branches,
		State: replayStateResponse{
			SessionID:   replayed.SessionState.SessionID,
			BranchHeads: replayed.SessionState.BranchHeads,
			LastEventID: replayed.SessionState.LastEventID,
			EventCount:  replayed.SessionState.EventCount,
		},
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	const route = "/healthz"
	s.cfg.Metrics.IncRequest(route)
	if r.Method != http.MethodGet {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	ctx, cancel := s.requestContext(r.Context())
	defer cancel()
	status := s.cfg.Backend.HealthCheck(ctx)
	diags := make([]backendDiagnostic, 0, len(status.Diagnostics))
	for _, d := range status.Diagnostics {
		diags = append(diags, backendDiagnostic{
			Code:    d.Code,
			Message: d.Message,
			Details: d.Details,
		})
	}
	writeJSON(w, http.StatusOK, healthzResponse{
		OK: status.OK,
		Backend: backendHealthPayload{
			OK:          status.OK,
			Diagnostics: diags,
		},
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	const route = "/metrics"
	s.cfg.Metrics.IncRequest(route)
	if r.Method != http.MethodGet {
		s.cfg.Metrics.IncError(route)
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, s.cfg.Metrics.Snapshot())
}

func (s *Server) startSession(sessionID, defaultBranchID string) (*runtime.Session, error) {
	return s.startSessionWithRunner(sessionID, defaultBranchID, s.cfg.ToolRunner)
}

func (s *Server) startSessionWithRunner(sessionID, defaultBranchID string, runner *tools.Runner) (*runtime.Session, error) {
	return runtime.StartSession(runtime.Config{
		SessionID:       sessionID,
		DefaultBranchID: defaultBranchID,
		Backend:         s.cfg.Backend,
		ModelAdapter:    s.cfg.ModelAdapter,
		ToolRunner:      runner,
		ModelHeaders:    s.cfg.ModelHeaders,
		Metrics:         s.cfg.Metrics,
	})
}

func (s *Server) requestContext(parent context.Context) (context.Context, context.CancelFunc) {
	if s.cfg.Timeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, s.cfg.Timeout)
}

func parseReplayPath(path string) (string, bool) {
	if !strings.HasPrefix(path, "/v1/session/") || !strings.HasSuffix(path, "/replay") {
		return "", false
	}
	parts := strings.Split(path, "/")
	// expected: ["", "v1", "session", "{id}", "replay"]
	if len(parts) != 5 {
		return "", false
	}
	sessionID := strings.TrimSpace(parts[3])
	if sessionID == "" {
		return "", false
	}
	return sessionID, true
}

func decodeJSONBody(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(payload)
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	if strings.TrimSpace(message) == "" {
		message = http.StatusText(status)
	}
	writeJSON(w, status, errorResponse{
		Error: errorBody{
			Code:    strings.TrimSpace(code),
			Message: message,
		},
	})
}

func normalizeAgentConstraints(raw map[string]any) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		key := strings.TrimSpace(k)
		if key == "" {
			return nil, errors.New("constraints keys must be non-empty")
		}
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("constraints[%s] must be JSON-encodable", key)
		}
		out[key] = string(encoded)
	}
	return out, nil
}

func buildAgentCallText(task string, refs []string, constraints map[string]string) (string, error) {
	task = strings.TrimSpace(task)
	if task == "" {
		return "", errors.New("task is required")
	}
	lines := []string{"task: " + task}
	if len(refs) > 0 {
		trimmed := make([]string, 0, len(refs))
		for _, ref := range refs {
			ref = strings.TrimSpace(ref)
			if ref == "" {
				continue
			}
			trimmed = append(trimmed, ref)
		}
		if len(trimmed) > 0 {
			lines = append(lines, "refs: "+strings.Join(trimmed, ", "))
		}
	}
	if len(constraints) > 0 {
		payload, err := json.Marshal(constraints)
		if err != nil {
			return "", errors.New("constraints must be valid JSON object")
		}
		lines = append(lines, "constraints: "+string(payload))
	}
	return strings.Join(lines, "\n"), nil
}

func buildConsultManifest(sessionID, branchID, fingerprint string, pf preflight.Result, task string) (consult.Manifest, error) {
	selected := make([]consult.SelectedItem, 0, len(pf.Refs))
	for _, ref := range pf.Refs {
		selected = append(selected, consult.SelectedItem{
			ID:    ref,
			Kind:  "ref",
			Ref:   ref,
			Bytes: len(ref),
		})
	}
	return consult.Generate(consult.Input{
		SessionID:     sessionID,
		BranchID:      branchID,
		Fingerprint:   fingerprint,
		Mode:          pf.Mode,
		Scope:         pf.Scope,
		Refs:          pf.Refs,
		Constraints:   pf.Constraints,
		SelectedItems: selected,
		ByteBudget:    assembler.MaxEnvelopeBytes,
		Compacted:     false,
		Truncated:     false,
		TaskSummary:   task,
	})
}

func (s *Server) findTrace(sessionID, branchID string) agentCallTrace {
	events, err := s.cfg.Backend.ListEventsByBranch(context.Background(), sessionID, branchID)
	if err != nil {
		return agentCallTrace{}
	}
	var trace agentCallTrace
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if trace.PacketEventID == "" && e.Kind == eventlog.KindPacketSent {
			trace.PacketEventID = e.ID
		}
		if trace.ResponseEventID == "" && e.Kind == eventlog.KindResponseReceived {
			trace.ResponseEventID = e.ID
		}
		if trace.PacketEventID != "" && trace.ResponseEventID != "" {
			break
		}
	}
	return trace
}

func writeCoalescedAgentCallResult(w http.ResponseWriter, out coalescedAgentCallResult) {
	if out.ErrorCode != "" {
		writeJSONError(w, out.Status, out.ErrorCode, out.ErrorMessage)
		return
	}
	if out.Response == nil {
		writeJSONError(w, http.StatusInternalServerError, "coalesced_result_missing", "missing coalesced response payload")
		return
	}
	writeJSON(w, out.Status, out.Response)
}
