package xs

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"

	"pancakes-harness/internal/backend"
	"pancakes-harness/internal/eventlog"
)

type Config struct {
	Command    string
	HealthArgs []string
}

type commandRunner func(ctx context.Context, command string, args ...string) ([]byte, error)

type Adapter struct {
	cfg    Config
	runCmd commandRunner

	mu       sync.RWMutex
	sessions map[string][]eventlog.Event
	byID     map[string]map[string]eventlog.Event
	blobs    map[string][]byte
	diag     []backend.Diagnostic
}

type Option func(*Adapter)

func WithCommandRunner(r commandRunner) Option {
	return func(a *Adapter) {
		a.runCmd = r
	}
}

func NewAdapter(cfg Config, opts ...Option) *Adapter {
	a := &Adapter{
		cfg:      cfg,
		runCmd:   defaultCommandRunner,
		sessions: make(map[string][]eventlog.Event),
		byID:     make(map[string]map[string]eventlog.Event),
		blobs:    make(map[string][]byte),
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

func (a *Adapter) AppendEvent(ctx context.Context, e eventlog.Event) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if err := e.Validate(); err != nil {
		return backend.ErrInvalidEvent
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.byID[e.SessionID] == nil {
		a.byID[e.SessionID] = make(map[string]eventlog.Event)
	}
	if _, exists := a.byID[e.SessionID][e.ID]; exists {
		return backend.ErrDuplicateEventID
	}
	copied := cloneEvent(e)
	a.sessions[e.SessionID] = append(a.sessions[e.SessionID], copied)
	a.byID[e.SessionID][e.ID] = copied
	return nil
}

func (a *Adapter) AppendBlob(ctx context.Context, ref string, payload []byte) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if ref == "" {
		return backend.ErrBlobNotFound
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.blobs[ref]; exists {
		return backend.ErrDuplicateBlobRef
	}
	a.blobs[ref] = append([]byte(nil), payload...)
	return nil
}

func (a *Adapter) GetEventByID(ctx context.Context, sessionID, eventID string) (eventlog.Event, error) {
	select {
	case <-ctx.Done():
		return eventlog.Event{}, ctx.Err()
	default:
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	s, ok := a.byID[sessionID]
	if !ok {
		return eventlog.Event{}, backend.ErrEventNotFound
	}
	e, ok := s[eventID]
	if !ok {
		return eventlog.Event{}, backend.ErrEventNotFound
	}
	return cloneEvent(e), nil
}

func (a *Adapter) ListEventsBySession(ctx context.Context, sessionID string) ([]eventlog.Event, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	stored := a.sessions[sessionID]
	out := make([]eventlog.Event, 0, len(stored))
	for _, e := range stored {
		out = append(out, cloneEvent(e))
	}
	return out, nil
}

func (a *Adapter) ListEventsByBranch(ctx context.Context, sessionID, branchID string) ([]eventlog.Event, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if branchID == "" {
		return nil, backend.ErrInvalidBranchRead
	}
	all, err := a.ListEventsBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]eventlog.Event, 0)
	for _, e := range all {
		if e.BranchID == branchID {
			out = append(out, e)
		}
	}
	return out, nil
}

func (a *Adapter) FetchBlob(ctx context.Context, ref string) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	data, ok := a.blobs[ref]
	if !ok {
		return nil, backend.ErrBlobNotFound
	}
	return append([]byte(nil), data...), nil
}

func (a *Adapter) HealthCheck(ctx context.Context) backend.HealthStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.diag = nil

	command := strings.TrimSpace(a.cfg.Command)
	if command == "" {
		a.diag = append(a.diag, backend.Diagnostic{
			Code:    "bad_config",
			Message: "xs command is not configured",
		})
		return backend.HealthStatus{OK: false, Diagnostics: cloneDiagnostics(a.diag)}
	}
	if _, err := exec.LookPath(command); err != nil {
		a.diag = append(a.diag, backend.Diagnostic{
			Code:    "xs_unavailable",
			Message: "xs command not found in PATH",
			Details: map[string]string{"command": command},
		})
		return backend.HealthStatus{OK: false, Diagnostics: cloneDiagnostics(a.diag)}
	}

	args := a.cfg.HealthArgs
	if len(args) == 0 {
		args = []string{"health"}
	}
	out, err := a.runCmd(ctx, command, args...)
	if err != nil {
		a.diag = append(a.diag, backend.Diagnostic{
			Code:    "health_check_failed",
			Message: "xs health command failed",
			Details: map[string]string{
				"command": command,
				"error":   err.Error(),
			},
		})
		return backend.HealthStatus{OK: false, Diagnostics: cloneDiagnostics(a.diag)}
	}

	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		trimmed = "ok"
	}
	a.diag = append(a.diag, backend.Diagnostic{
		Code:    "healthy",
		Message: "xs adapter healthy",
		Details: map[string]string{"output": trimmed},
	})
	return backend.HealthStatus{OK: true, Diagnostics: cloneDiagnostics(a.diag)}
}

func (a *Adapter) LastDiagnostics() []backend.Diagnostic {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return cloneDiagnostics(a.diag)
}

func (a *Adapter) ClearDiagnostics() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.diag = nil
}

func defaultCommandRunner(ctx context.Context, command string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, errors.New(strings.TrimSpace(err.Error()))
	}
	return out, nil
}

func cloneEvent(in eventlog.Event) eventlog.Event {
	out := in
	if in.Refs != nil {
		out.Refs = append([]string(nil), in.Refs...)
	}
	if in.Meta != nil {
		out.Meta = make(map[string]any, len(in.Meta))
		for k, v := range in.Meta {
			out.Meta[k] = v
		}
	}
	return out
}

func cloneDiagnostics(in []backend.Diagnostic) []backend.Diagnostic {
	out := make([]backend.Diagnostic, 0, len(in))
	for _, d := range in {
		cp := d
		if d.Details != nil {
			cp.Details = make(map[string]string, len(d.Details))
			for k, v := range d.Details {
				cp.Details[k] = v
			}
		}
		out = append(out, cp)
	}
	return out
}
