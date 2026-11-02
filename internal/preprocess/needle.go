// Package preprocess — Needle 3 adapter for the fast model sidecar.
//
// Implements FastAdapter by driving a local Needle 3 process instead of an
// HTTP API: scripts/needle_bridge.py speaks JSON lines on stdin/stdout, does
// the extraction with a 2-bit 8-29 MB model, and gates the result with laya
// (System One). No API key and no network at inference time — the weights are
// a .cact file mapped in place.
//
// The exchange is one request per line, answered in order, so a crash or a
// wedge costs the in-flight call and nothing else: the process is killed and
// respawned on the next Call.
package preprocess

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultNeedleTimeout is the per-extraction cap. A local Needle 3 turn is
	// 600-900 ms warm; the cap exists for a wedged process, not for slowness.
	DefaultNeedleTimeout = 20 * time.Second
)

// NeedleConfig configures the local Needle 3 fast adapter.
type NeedleConfig struct {
	// Python is the interpreter that runs the bridge. Default: $NEEDLE_PYTHON,
	// then "python3".
	Python string
	// Bridge is the path to scripts/needle_bridge.py. Default: $NEEDLE_BRIDGE.
	// Required — the harness does not guess where its checkout lives.
	Bridge string
	// Timeout caps a single extraction call. Default: DefaultNeedleTimeout.
	Timeout time.Duration
}

// NeedleAdapter implements FastAdapter over a long-lived bridge process.
type NeedleAdapter struct {
	python  string
	bridge  string
	timeout time.Duration

	mu     sync.Mutex
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	seq    int
}

// NewNeedleAdapter validates the configuration. The bridge process itself is
// started on the first call, so construction cannot fail on a missing weight
// file that has not been fetched yet.
func NewNeedleAdapter(cfg NeedleConfig) (*NeedleAdapter, error) {
	if cfg.Python == "" {
		cfg.Python = os.Getenv("NEEDLE_PYTHON")
	}
	if cfg.Python == "" {
		cfg.Python = "python3"
	}
	if cfg.Bridge == "" {
		cfg.Bridge = os.Getenv("NEEDLE_BRIDGE")
	}
	if cfg.Bridge == "" {
		return nil, errors.New("needle: bridge script required (set NEEDLE_BRIDGE or pass NeedleConfig.Bridge)")
	}
	if _, err := os.Stat(cfg.Bridge); err != nil {
		return nil, fmt.Errorf("needle: bridge script: %w", err)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultNeedleTimeout
	}
	return &NeedleAdapter{
		python:  cfg.Python,
		bridge:  cfg.Bridge,
		timeout: cfg.Timeout,
	}, nil
}

// Name identifies the adapter on the event spine.
func (a *NeedleAdapter) Name() string { return "needle3" }

type needleRequest struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Prompt string `json:"prompt,omitempty"`
}

type needleResponse struct {
	ID         string          `json:"id,omitempty"`
	OK         bool            `json:"ok"`
	Extraction json.RawMessage `json:"extraction,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// Call runs one extraction and returns the raw extraction object for
// parseExtraction to type and validate.
func (a *NeedleAdapter) Call(ctx context.Context, prompt, text string) ([]byte, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("needle: empty text")
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if err := a.ensureLocked(); err != nil {
		return nil, err
	}

	a.seq++
	line, err := json.Marshal(needleRequest{ID: fmt.Sprintf("r%d", a.seq), Text: text, Prompt: prompt})
	if err != nil {
		return nil, fmt.Errorf("needle: encode request: %w", err)
	}

	type outcome struct {
		line []byte
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		if _, writeErr := a.stdin.Write(append(line, '\n')); writeErr != nil {
			done <- outcome{err: writeErr}
			return
		}
		reply, readErr := a.stdout.ReadBytes('\n')
		done <- outcome{line: reply, err: readErr}
	}()

	timeout := a.timeout
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < timeout {
			timeout = remaining
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case out := <-done:
		if out.err != nil {
			// A dead or desynchronized process is replaced, not patched up.
			a.closeLocked()
			return nil, fmt.Errorf("needle: exchange: %w", out.err)
		}
		return a.parseReply(out.line)
	case <-timer.C:
		a.closeLocked()
		<-done
		return nil, fmt.Errorf("needle: extraction timed out after %s", timeout)
	case <-ctx.Done():
		a.closeLocked()
		<-done
		return nil, fmt.Errorf("needle: %w", ctx.Err())
	}
}

func (a *NeedleAdapter) parseReply(line []byte) ([]byte, error) {
	var resp needleResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		a.closeLocked()
		return nil, fmt.Errorf("needle: decode reply: %w", err)
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "bridge reported failure"
		}
		return nil, errors.New("needle: " + resp.Error)
	}
	if len(resp.Extraction) == 0 {
		return nil, errors.New("needle: reply carried no extraction")
	}
	return resp.Extraction, nil
}

// Close stops the bridge process. Safe to call more than once.
func (a *NeedleAdapter) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closeLocked()
	return nil
}

func (a *NeedleAdapter) ensureLocked() error {
	if a.cmd != nil {
		return nil
	}
	cmd := exec.Command(a.python, a.bridge)
	cmd.Stderr = os.Stderr // bridge diagnostics stay visible in the serve log
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("needle: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("needle: stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("needle: start bridge: %w", err)
	}
	a.cmd = cmd
	a.stdin = stdin
	a.stdout = bufio.NewReaderSize(stdout, 1<<16)
	return nil
}

func (a *NeedleAdapter) closeLocked() {
	if a.cmd == nil {
		return
	}
	if a.stdin != nil {
		_ = a.stdin.Close()
	}
	if a.cmd.Process != nil {
		_ = a.cmd.Process.Kill()
	}
	_ = a.cmd.Wait()
	a.cmd = nil
	a.stdin = nil
	a.stdout = nil
}
