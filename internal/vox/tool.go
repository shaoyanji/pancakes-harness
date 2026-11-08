// Package vox is an explainer harness: a small on-device caller picks one tool
// call, a decision model routes what to do with it, and a chat lane says the
// result back to a person. Neither half is useful alone — a caller with no
// voice answers in JSON, and a chat model with no tools cannot look anything up.
package vox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var (
	ErrNoCaller     = errors.New("no tool caller configured")
	ErrUnknownTool  = errors.New("the caller named a tool that is not in the manifest")
	ErrMissingArg   = errors.New("the call is missing a required argument")
	ErrUngrounded   = errors.New("the caller flagged its own argument as not in the input")
	ErrWriteRefused = errors.New("a tool that is not read-only needs an explicit allow")
	ErrNoVoice      = errors.New("no chat lane configured")
	ErrNoDecision   = errors.New("no decision model configured")
)

// Tool is one entry of the call surface. Command is an argv template, never a
// shell string: a value with a space in it stays one argument, which is the
// difference between a lookup and a task that prints its own help.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Command     []string        `json:"command"`
	ReadOnly    bool            `json:"read_only"`
}

// Manifest is the tool surface, in the order the caller sees it.
type Manifest struct {
	Tools []Tool
}

// LoadManifest reads either a bare array (the shape needle takes) or an object
// with a `tools` field (the shape a hand-written surface usually has).
func LoadManifest(path string) (Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("read tool manifest: %w", err)
	}
	var direct []Tool
	if err := json.Unmarshal(raw, &direct); err != nil {
		var wrapper struct {
			Tools []Tool `json:"tools"`
		}
		if err := json.Unmarshal(raw, &wrapper); err != nil {
			return Manifest{}, fmt.Errorf("parse tool manifest: %w", err)
		}
		direct = wrapper.Tools
	}
	m := Manifest{Tools: direct}
	if err := m.validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func (m Manifest) validate() error {
	if len(m.Tools) == 0 {
		return errors.New("the tool manifest is empty")
	}
	seen := make(map[string]bool, len(m.Tools))
	for i, t := range m.Tools {
		if strings.TrimSpace(t.Name) == "" {
			return fmt.Errorf("tool %d has no name", i)
		}
		if seen[t.Name] {
			return fmt.Errorf("tool %q is declared twice", t.Name)
		}
		seen[t.Name] = true
		if len(t.Command) == 0 {
			return fmt.Errorf("tool %q has no command", t.Name)
		}
	}
	return nil
}

func (m Manifest) byName(name string) (Tool, bool) {
	for _, t := range m.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// Writable counts the tools a caller may not run without an explicit allow.
func (m Manifest) Writable() int {
	n := 0
	for _, t := range m.Tools {
		if !t.ReadOnly {
			n++
		}
	}
	return n
}

// Call is one validated tool call. Calls is the number the caller proposed:
// more than one means the request was reducible, which is not the caller's job.
type Call struct {
	Tool       string
	Args       map[string]string
	Confidence float64
	Calls      int
}

type wireCall struct {
	FunctionCalls []struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"function_calls"`
	Confidence float64 `json:"confidence"`
	Validation struct {
		Ungrounded []string `json:"ungrounded"`
	} `json:"validation"`
}

// ParseCall validates caller output against the manifest. Validation comes
// before confidence: an invented tool is a refusal whatever the score says, and
// an ungrounded argument is the caller admitting its own value is not in the
// prompt. Confidence is reported, never used as the gate.
func ParseCall(raw []byte, m Manifest) (Call, bool, error) {
	var w wireCall
	if err := json.Unmarshal(raw, &w); err != nil {
		return Call{}, false, fmt.Errorf("parse caller output: %w", err)
	}
	if len(w.Validation.Ungrounded) > 0 {
		return Call{}, false, fmt.Errorf("%w: %s", ErrUngrounded, strings.Join(w.Validation.Ungrounded, ","))
	}
	if len(w.FunctionCalls) == 0 {
		return Call{}, false, nil
	}
	fc := w.FunctionCalls[0]
	tool, ok := m.byName(fc.Name)
	if !ok {
		return Call{}, false, fmt.Errorf("%w: %s", ErrUnknownTool, fc.Name)
	}
	args := make(map[string]string, len(fc.Arguments))
	for k, v := range fc.Arguments {
		switch typed := v.(type) {
		case string:
			args[k] = typed
		case nil:
			args[k] = ""
		default:
			args[k] = fmt.Sprint(typed)
		}
	}
	for _, req := range requiredArgs(tool) {
		if _, ok := args[req]; !ok {
			return Call{}, false, fmt.Errorf("%w: %s.%s", ErrMissingArg, tool.Name, req)
		}
	}
	return Call{
		Tool:       tool.Name,
		Args:       args,
		Confidence: w.Confidence,
		Calls:      len(w.FunctionCalls),
	}, true, nil
}

func requiredArgs(t Tool) []string {
	if len(t.Parameters) == 0 {
		return nil
	}
	var p struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(t.Parameters, &p); err != nil {
		return nil
	}
	return p.Required
}

var placeholder = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

// Execute runs the tool with the call's arguments substituted into its argv
// template. A missing placeholder is an error rather than an empty argument: a
// lookup with a hole in it returns the wrong answer quietly.
func (t Tool) Execute(ctx context.Context, args map[string]string, allowWrite bool) (string, error) {
	if !t.ReadOnly && !allowWrite {
		return "", fmt.Errorf("%w: %s", ErrWriteRefused, t.Name)
	}
	argv := make([]string, 0, len(t.Command))
	for _, part := range t.Command {
		missing := ""
		resolved := placeholder.ReplaceAllStringFunc(part, func(m string) string {
			key := m[1 : len(m)-1]
			v, ok := args[key]
			if !ok {
				missing = key
				return ""
			}
			return v
		})
		if missing != "" {
			return "", fmt.Errorf("%w: %s", ErrMissingArg, missing)
		}
		argv = append(argv, resolved)
	}
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("run %s: %s", t.Name, truncate(msg, 400))
	}
	return strings.TrimSpace(string(out)), nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
