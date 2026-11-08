package vox

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// Voice is the human-loop lane: it turns a tool's output into a sentence a
// person can read, and it is the only layer that writes prose.
type Voice interface {
	Name() string
	Say(ctx context.Context, prompt string) (string, error)
}

const (
	DefaultVoiceModel   = "gpt-6-luna"
	DefaultVoiceSession = "vox"
	DefaultVoiceTimeout = 90 * time.Second
	// MaxExplainChars bounds what the voice is asked to explain. The API refuses
	// a 20k message outright (measured: MESSAGE_TOO_LONG, max 15000), and a tool
	// that returns more than this is a report, not an explanation.
	MaxExplainChars = 6000
)

// CPGVoice is one chat model on chatplayground, addressed through the `cpg`
// client's one-shot lane. It is stateless here on purpose: the explainer sends
// one packet per question and the thread lives server side.
type CPGVoice struct {
	Bin     string
	Model   string
	Session string
	Timeout time.Duration
}

func (c CPGVoice) Name() string {
	if c.Bin == "" {
		return "cpg (missing)"
	}
	return "cpg:" + c.model()
}

func (c CPGVoice) model() string {
	if c.Model == "" {
		return DefaultVoiceModel
	}
	return c.Model
}

func (c CPGVoice) Say(ctx context.Context, prompt string) (string, error) {
	if c.Bin == "" {
		return "", ErrNoVoice
	}
	session := c.Session
	if session == "" {
		session = DefaultVoiceSession
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultVoiceTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, c.Bin, "chat", "-m", c.model(), "-s", session, prompt)
	var stderr lineBuffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", &VoiceError{Err: err, Detail: truncate(stderr.String(), 300)}
	}
	return strings.TrimSpace(string(out)), nil
}

// VoiceError carries the lane's own diagnostic, because a 522 from a stale
// binary and a 401 from a dead token are the same exit code and different fixes.
type VoiceError struct {
	Err    error
	Detail string
}

func (v *VoiceError) Error() string { return "chat lane: " + v.Err.Error() + ": " + v.Detail }
func (v *VoiceError) Unwrap() error { return v.Err }

// StaticVoice is a recorded answer, for tests and for `-voice off` parity.
type StaticVoice struct {
	Text string
}

func (StaticVoice) Name() string { return "static" }

func (s StaticVoice) Say(_ context.Context, _ string) (string, error) { return s.Text, nil }

// ExplainPrompt is the only prompt this harness writes for prose: state the
// question, the call that was made, and the output, and forbid invention. The
// packet is bounded before it is sent, never truncated silently afterwards.
func ExplainPrompt(question, tool string, args map[string]string, output string) string {
	var b strings.Builder
	b.WriteString("A tool was just run to answer a question. Explain the result to the person who asked, in at most four sentences.\n")
	b.WriteString("Use only what the output says. Do not add facts, do not guess missing values, and say plainly if the output does not answer the question.\n\n")
	b.WriteString("QUESTION: ")
	b.WriteString(strings.TrimSpace(question))
	b.WriteString("\n\nCALL: ")
	b.WriteString(tool)
	if len(args) > 0 {
		b.WriteString("(")
		first := true
		for k, v := range args {
			if !first {
				b.WriteString(", ")
			}
			b.WriteString(k)
			b.WriteString("=")
			b.WriteString(v)
			first = false
		}
		b.WriteString(")")
	}
	b.WriteString("\n\nOUTPUT:\n")
	b.WriteString(truncate(output, MaxExplainChars))
	return b.String()
}
