package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"pancakes-harness/internal/assembler"
	"pancakes-harness/internal/backend"
	"pancakes-harness/internal/model"
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
}

type Server struct {
	cfg Config
}

func New(cfg Config) (*Server, error) {
	if cfg.Backend == nil || cfg.ModelAdapter == nil {
		return nil, ErrInvalidConfig
	}
	if cfg.ToolRunner == nil {
		cfg.ToolRunner = tools.NewRunner(nil)
	}
	return &Server{cfg: cfg}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/v1/turn", s.handleTurn)
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
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	var req turnRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.BranchID = strings.TrimSpace(req.BranchID)
	req.Text = strings.TrimSpace(req.Text)
	if req.SessionID == "" || req.Text == "" {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "session_id and text are required")
		return
	}
	branch := req.BranchID
	if branch == "" {
		branch = "main"
	}

	session, err := s.startSession(req.SessionID, branch)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "runtime_error", err.Error())
		return
	}
	ctx, cancel := s.requestContext(r.Context())
	defer cancel()
	res, err := session.HandleUserTurn(ctx, branch, req.Text)
	if err != nil {
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

func (s *Server) handleBranchFork(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	var req branchForkRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.ParentBranchID = strings.TrimSpace(req.ParentBranchID)
	req.ChildBranchID = strings.TrimSpace(req.ChildBranchID)
	if req.SessionID == "" || req.ParentBranchID == "" || req.ChildBranchID == "" {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "session_id, parent_branch_id, and child_branch_id are required")
		return
	}

	session, err := s.startSession(req.SessionID, req.ParentBranchID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "runtime_error", err.Error())
		return
	}
	ctx, cancel := s.requestContext(r.Context())
	defer cancel()
	if err := session.ForkBranch(ctx, req.ParentBranchID, req.ChildBranchID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "fork_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, branchForkResponse{OK: true})
}

func (s *Server) handleSessionReplay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	sessionID, ok := parseReplayPath(r.URL.Path)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not_found", "route not found")
		return
	}

	session, err := s.startSession(sessionID, "main")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "runtime_error", err.Error())
		return
	}
	ctx, cancel := s.requestContext(r.Context())
	defer cancel()
	replayed, err := session.ReplaySession(ctx)
	if err != nil {
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
	if r.Method != http.MethodGet {
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

func (s *Server) startSession(sessionID, defaultBranchID string) (*runtime.Session, error) {
	return runtime.StartSession(runtime.Config{
		SessionID:       sessionID,
		DefaultBranchID: defaultBranchID,
		Backend:         s.cfg.Backend,
		ModelAdapter:    s.cfg.ModelAdapter,
		ToolRunner:      s.cfg.ToolRunner,
		ModelHeaders:    s.cfg.ModelHeaders,
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
