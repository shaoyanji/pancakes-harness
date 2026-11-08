package vox

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Caller turns one sentence into at most one validated tool call.
type Caller interface {
	Name() string
	Call(ctx context.Context, prompt string, m Manifest) (Call, bool, error)
}

// DefaultCallerTimeout bounds a caller that has no clock of its own.
const DefaultCallerTimeout = 60 * time.Second

// NeedleCaller is needle3: 8-29 MB, on-device, one JSON call out. Its shipped
// binary is linux-arm64, so on any other platform this returns ErrNoCaller and
// the caller falls back to ReplayCaller or to a chat model.
type NeedleCaller struct {
	Engine  string
	Weights string
	Timeout time.Duration
	// Env is appended after the telemetry defaults, so a test can point the
	// engine at a fixture without the binary phoning home.
	Env []string
}

func (n NeedleCaller) Name() string {
	if n.Engine == "" || n.Weights == "" {
		return "needle3 (missing)"
	}
	return "needle3"
}

// needleTool is what the engine sees: name, description and JSON schema only.
// A caller this small has no room for `command` or `read_only`, and handing it
// a surface it cannot use is how a call starts naming fields as tools.
type needleTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

func (n NeedleCaller) Call(ctx context.Context, prompt string, m Manifest) (Call, bool, error) {
	if n.Engine == "" || n.Weights == "" {
		return Call{}, false, ErrNoCaller
	}
	spec := make([]needleTool, 0, len(m.Tools))
	for _, t := range m.Tools {
		spec = append(spec, needleTool{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	body, err := json.Marshal(spec)
	if err != nil {
		return Call{}, false, fmt.Errorf("encode tool surface: %w", err)
	}
	dir, err := os.MkdirTemp("", "vox-tools-")
	if err != nil {
		return Call{}, false, fmt.Errorf("tool surface temp dir: %w", err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "tools.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return Call{}, false, fmt.Errorf("write tool surface: %w", err)
	}

	timeout := n.Timeout
	if timeout <= 0 {
		timeout = DefaultCallerTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, n.Engine,
		"--model", n.Weights,
		"--tools", path,
		"--prompt", prompt,
	)
	cmd.Env = append(os.Environ(), "NEEDLE_TELEMETRY=0", "DO_NOT_TRACK=1")
	cmd.Env = append(cmd.Env, n.Env...)
	var stderr lineBuffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return Call{}, false, fmt.Errorf("needle: %v: %s", err, truncate(stderr.String(), 300))
	}
	return ParseCall(out, m)
}

// ReplayCaller answers from a recorded call instead of an engine. It exists
// because the engine is arm64-only: every x86_64 test target still exercises
// the whole loop, and a recorded round stays reproducible.
type ReplayCaller struct {
	Path string
}

func (r ReplayCaller) Name() string { return "replay" }

func (r ReplayCaller) Call(_ context.Context, _ string, m Manifest) (Call, bool, error) {
	if r.Path == "" {
		return Call{}, false, ErrNoCaller
	}
	raw, err := os.ReadFile(r.Path)
	if err != nil {
		return Call{}, false, fmt.Errorf("read replay: %w", err)
	}
	return ParseCall(raw, m)
}

// lineBuffer keeps the last few hundred bytes of a child's stderr, which is all
// a diagnostic needs and all an error message can carry.
type lineBuffer struct {
	b []byte
}

func (l *lineBuffer) Write(p []byte) (int, error) {
	l.b = append(l.b, p...)
	if len(l.b) > 4096 {
		l.b = l.b[len(l.b)-4096:]
	}
	return len(p), nil
}

func (l *lineBuffer) String() string { return string(l.b) }
