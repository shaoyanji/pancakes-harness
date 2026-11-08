package vox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Choice is one described option. Label is what comes back, Text is what the
// model reads — a label alone tells it nothing.
type Choice struct {
	Label string
	Text  string
}

// Verdict is a decision, never an authorisation. Status is `selected` or
// `needs_review`; the probability is the model's own distribution and does not
// transfer to another provider.
type Verdict struct {
	Status      string
	Pick        string
	Probability float64
	Margin      float64
}

func (v Verdict) Selected() bool { return v.Status == "selected" }

// LaneRequest is the state plus the options, which is all a decision model gets.
type LaneRequest struct {
	State        string
	Question     string
	Instructions string
	Choices      []Choice
}

// Decision routes a request to a lane. It returns a label, never prose.
type Decision interface {
	Name() string
	Choose(ctx context.Context, req LaneRequest) (Verdict, error)
}

const DefaultDecisionTimeout = 30 * time.Second

// JevDecision calls `jev-decide decide --provider <p> <request.json>`. The
// provider is explicit: the CLI's default is OpenRouter, which answers 402, and
// a silent default turns a decision into a payment error.
type JevDecision struct {
	Bin      string
	Provider string
	Timeout  time.Duration
}

func (j JevDecision) Name() string {
	if j.Bin == "" {
		return "jev (missing)"
	}
	return "jev"
}

// Request is exported so a doctor run can validate the shape without spending a
// call (`jev-decide decide --dry-run`).
func (r LaneRequest) Request() map[string]any {
	criteria := make(map[string]string, len(r.Choices))
	for _, c := range r.Choices {
		criteria[c.Label] = c.Text
	}
	state := map[string]any{"question": r.Question}
	if r.State != "" {
		state["context"] = r.State
	}
	return map[string]any{
		"state": state,
		"questions": map[string]any{
			"lane": map[string]any{
				"type":         "choice",
				"instructions": r.Instructions,
				"criteria":     criteria,
			},
		},
	}
}

type jevResponse struct {
	Decisions map[string]struct {
		Status      string  `json:"status"`
		Value       string  `json:"value"`
		Probability float64 `json:"probability"`
		Margin      float64 `json:"margin"`
	} `json:"decisions"`
}

func (j JevDecision) Choose(ctx context.Context, req LaneRequest) (Verdict, error) {
	if j.Bin == "" {
		return Verdict{}, ErrNoDecision
	}
	provider := j.Provider
	if provider == "" {
		provider = "typesafe"
	}
	body, err := json.Marshal(req.Request())
	if err != nil {
		return Verdict{}, fmt.Errorf("encode decision request: %w", err)
	}
	dir, err := os.MkdirTemp("", "vox-decide-")
	if err != nil {
		return Verdict{}, fmt.Errorf("decision temp dir: %w", err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "request.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return Verdict{}, fmt.Errorf("write decision request: %w", err)
	}

	timeout := j.Timeout
	if timeout <= 0 {
		timeout = DefaultDecisionTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, j.Bin, "decide", "--provider", provider, path)
	var stderr lineBuffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()

	// Exit 2 is `needs_review`: a usable answer that must not act on its own.
	// Anything else that failed is a refusal, never a defaulted label.
	var exitErr *exec.ExitError
	needsReview := false
	if err != nil {
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
			return Verdict{}, fmt.Errorf("jev: %v: %s", err, truncate(stderr.String(), 300))
		}
		needsReview = true
	}

	var resp jevResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return Verdict{}, fmt.Errorf("parse decision: %w: %s", err, truncate(string(out), 200))
	}
	lane, ok := resp.Decisions["lane"]
	if !ok {
		return Verdict{}, fmt.Errorf("decision carried no lane: %s", truncate(string(out), 200))
	}
	status := lane.Status
	if needsReview {
		status = "needs_review"
	}
	return Verdict{Status: status, Pick: lane.Value, Probability: lane.Probability, Margin: lane.Margin}, nil
}

// FixedDecision is a decision model with no model in it, for tests and for a
// `doctor` run that must not spend a call.
type FixedDecision struct {
	Label       string
	Review      bool
	Probability float64
	Margin      float64
}

func (FixedDecision) Name() string { return "fixed" }

func (f FixedDecision) Choose(_ context.Context, _ LaneRequest) (Verdict, error) {
	status := "selected"
	if f.Review {
		status = "needs_review"
	}
	return Verdict{Status: status, Pick: f.Label, Probability: f.Probability, Margin: f.Margin}, nil
}
