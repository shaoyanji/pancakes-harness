package vox

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Result kinds. They are the CLI's exit codes too, so a script never parses
// prose to learn what happened.
const (
	KindAnswered = "answered"
	KindActed    = "acted"
	KindConfirm  = "confirm"
	KindEscalate = "escalate"
	KindDeclined = "declined"
)

// Lane labels the decision model chooses between.
const (
	LaneRunTool      = "run_tool"
	LaneAnswer       = "answer_directly"
	LaneEscalate     = "escalate"
	laneInstructions = "Pick the lane that answers the question with the least invented content. run_tool is correct when the named tool's output answers it; answer_directly when no lookup is needed; escalate when the request needs a reasoning model, a plan, or more than one lookup."
	defaultMinProb   = 0.8
	defaultMinMargin = 0.15
)

// Engine is the whole harness: a surface, a caller, a router and a voice.
type Engine struct {
	Manifest   Manifest
	Caller     Caller
	Decision   Decision
	Voice      Voice
	AllowWrite bool
	MinProb    float64
	MinMargin  float64
}

// Result is the one object the CLI prints, human or JSON.
type Result struct {
	Kind        string            `json:"decision"`
	Lane        string            `json:"lane,omitempty"`
	Tool        string            `json:"tool,omitempty"`
	Args        map[string]string `json:"args,omitempty"`
	Output      string            `json:"output,omitempty"`
	Explanation string            `json:"explanation,omitempty"`
	Reason      string            `json:"reason,omitempty"`
	Confidence  float64           `json:"confidence"`
	Probability float64           `json:"probability"`
	Margin      float64           `json:"margin"`
	Caller      string            `json:"caller"`
	Decision    string            `json:"decision_model"`
	Voice       string            `json:"voice"`
}

// Ask is one turn: call, route, run, explain. A refusal is a Result with a
// reason, because "nothing happened, and here is why" is an answer.
func (e Engine) Ask(ctx context.Context, question string) (Result, error) {
	res := Result{
		Confidence: 0,
		Caller:     nameOf(e.Caller),
		Decision:   nameOf(e.Decision),
		Voice:      nameOf(e.Voice),
	}

	call, ok, err := e.Caller.Call(ctx, question, e.Manifest)
	if err != nil {
		switch {
		case errors.Is(err, ErrUnknownTool), errors.Is(err, ErrUngrounded), errors.Is(err, ErrMissingArg):
			res.Kind = KindDeclined
			res.Reason = err.Error()
			return res, nil
		case errors.Is(err, ErrNoCaller):
			// No engine on this platform (it ships arm64 only). The question is
			// still answerable, but only by a chat lane — and answerWithoutCall
			// checks that one exists instead of assuming it.
			res.Reason = "no tool caller on this host: answered from the chat lane alone"
			return e.answerWithoutCall(ctx, question, res)
		default:
			return res, err
		}
	}

	if !ok {
		return e.answerWithoutCall(ctx, question, res)
	}
	res.Confidence = call.Confidence
	if call.Calls > 1 {
		// The Pareto rule: a caller is for one deterministic lookup. A request
		// that decomposes belongs to a planner, so it is escalated, not merged.
		res.Kind = KindEscalate
		res.Reason = fmt.Sprintf("the request decomposed into %d calls; a caller is not a planner", call.Calls)
		return res, nil
	}

	choices := []Choice{
		{Label: "c0", Text: LaneRunTool + ": run " + call.Tool + " with the arguments the caller proposed"},
		{Label: "c1", Text: LaneAnswer + ": answer from the prompt alone, with no lookup"},
		{Label: "c2", Text: LaneEscalate + ": hand the request to a reasoning model"},
	}
	lane, err := e.route(ctx, question, question, choices, LaneRunTool)
	if err != nil {
		return res, err
	}
	laneName := resolveLane(lane.Pick, choices)
	res.Lane = laneName
	res.Probability, res.Margin = lane.Probability, lane.Margin

	if !e.confident(lane) {
		res.Kind = KindConfirm
		res.Tool, res.Args = call.Tool, call.Args
		res.Reason = fmt.Sprintf("the lane is %s at p=%.2f m=%.2f; a decision below the gate is a proposal, not an action", laneName, lane.Probability, lane.Margin)
		return res, nil
	}

	switch laneName {
	case LaneRunTool:
		return e.runTool(ctx, question, call, res)
	case LaneAnswer:
		return e.speak(ctx, question, res)
	case LaneEscalate:
		res.Kind = KindEscalate
		res.Reason = "the lane asked for a reasoning model"
		return res, nil
	default:
		res.Kind = KindConfirm
		res.Tool, res.Args = call.Tool, call.Args
		res.Reason = "the lane named an option this harness cannot carry out: " + lane.Pick
		return res, nil
	}
}

func (e Engine) answerWithoutCall(ctx context.Context, question string, res Result) (Result, error) {
	if e.Voice == nil {
		res.Kind = KindDeclined
		res.Reason = "the caller proposed no call and there is no chat lane to answer"
		return res, nil
	}
	choices := []Choice{
		{Label: "c0", Text: LaneAnswer + ": the caller found no lookup and a chat model answers from the prompt"},
		{Label: "c1", Text: LaneEscalate + ": the request needs a reasoning model"},
	}
	lane, err := e.route(ctx, question, question, choices, LaneAnswer)
	if err != nil {
		return res, err
	}
	laneName := resolveLane(lane.Pick, choices)
	res.Lane = laneName
	res.Probability, res.Margin = lane.Probability, lane.Margin
	if laneName == LaneEscalate {
		res.Kind = KindEscalate
		res.Reason = "no tool call, and the lane asked for a reasoning model"
		return res, nil
	}
	if !e.confident(lane) {
		res.Kind = KindConfirm
		res.Reason = "no tool call, and the lane is below the gate"
		return res, nil
	}
	return e.speak(ctx, question, res)
}

func (e Engine) runTool(ctx context.Context, question string, call Call, res Result) (Result, error) {
	tool, ok := e.Manifest.byName(call.Tool)
	if !ok {
		return res, fmt.Errorf("%w: %s", ErrUnknownTool, call.Tool)
	}
	if !tool.ReadOnly && !e.AllowWrite {
		res.Kind = KindConfirm
		res.Tool, res.Args = call.Tool, call.Args
		res.Reason = "this tool is not read-only; re-run with --allow-write to execute it"
		return res, nil
	}
	out, err := tool.Execute(ctx, call.Args, e.AllowWrite)
	if err != nil {
		return res, err
	}
	res.Kind = KindActed
	res.Tool, res.Args, res.Output = call.Tool, call.Args, out
	if e.Voice == nil {
		return res, nil
	}
	said, err := e.Voice.Say(ctx, ExplainPrompt(question, call.Tool, call.Args, out))
	if err != nil {
		// The tool answer is the answer; a dead voice costs prose, never the
		// result. The reason is reported instead of swallowed.
		res.Reason = err.Error()
		return res, nil
	}
	res.Explanation = said
	return res, nil
}

func (e Engine) speak(ctx context.Context, question string, res Result) (Result, error) {
	if e.Voice == nil {
		res.Kind = KindDeclined
		res.Reason = "the lane asked for a direct answer and no chat lane is configured"
		return res, nil
	}
	said, err := e.Voice.Say(ctx, question)
	if err != nil {
		res.Kind = KindDeclined
		res.Reason = err.Error()
		return res, nil
	}
	res.Kind = KindAnswered
	res.Explanation = said
	return res, nil
}

func (e Engine) route(ctx context.Context, question, state string, choices []Choice, fallback string) (Verdict, error) {
	if e.Decision == nil {
		return Verdict{Status: "selected", Pick: fallback, Probability: 1, Margin: 1}, nil
	}
	return e.Decision.Choose(ctx, LaneRequest{
		State:        state,
		Question:     question,
		Instructions: laneInstructions,
		Choices:      choices,
	})
}

// resolveLane maps a decision's pick back to a lane name. A decision model
// answers with the criteria KEY (`c0`), not the text it was given, so the label
// is looked up; a model that answers with the lane name itself is accepted too.
func resolveLane(pick string, choices []Choice) string {
	for _, c := range choices {
		if c.Label == pick {
			lane, _, _ := strings.Cut(c.Text, ":")
			return strings.TrimSpace(lane)
		}
	}
	return strings.TrimSpace(pick)
}

func (e Engine) confident(v Verdict) bool {
	if !v.Selected() {
		return false
	}
	prob := e.MinProb
	if prob == 0 {
		prob = defaultMinProb
	}
	margin := e.MinMargin
	if margin == 0 {
		margin = defaultMinMargin
	}
	return v.Probability >= prob && v.Margin >= margin
}

func nameOf(v any) string {
	switch typed := v.(type) {
	case Caller:
		if typed == nil {
			return ""
		}
		return typed.Name()
	case Decision:
		if typed == nil {
			return ""
		}
		return typed.Name()
	case Voice:
		if typed == nil {
			return ""
		}
		return typed.Name()
	default:
		return ""
	}
}
