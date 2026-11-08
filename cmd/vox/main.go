// vox — the explainer CLI: one caller, one decision, one voice.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"pancakes-harness/internal/vox"
)

const releaseVersion = "0.1.0"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage())
		return 0
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "ask":
		return runAsk(args[1:], stdin, stdout, stderr, getenv)
	case "decide":
		return runDecide(args[1:], stdin, stdout, stderr, getenv)
	case "say":
		return runSay(args[1:], stdin, stdout, stderr, getenv)
	case "tools":
		return runTools(args[1:], stdout, stderr, getenv)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr, getenv)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage())
		return 0
	case "version", "-version", "--version":
		fmt.Fprintf(stdout, "vox %s\n", releaseVersion)
		return 0
	default:
		fmt.Fprintf(stderr, "vox: unknown command %q\n\n%s", args[0], usage())
		return 64
	}
}

func runAsk(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "print the result as JSON")
	allowWrite := fs.Bool("allow-write", false, "execute tools that are not read-only")
	voiceOn := fs.Bool("voice", true, "explain the result through the chat lane")
	replay := fs.String("replay", "", "answer from a recorded caller output instead of an engine")
	toolsPath := fs.String("tools", "", "tool manifest (default $VOX_TOOLS or ./tools.json)")
	timeout := fs.Duration("timeout", 3*time.Minute, "whole-turn timeout")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "vox ask: %v\n", err)
		return 64
	}
	question := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if question == "" {
		raw, _ := io.ReadAll(stdin)
		question = strings.TrimSpace(string(raw))
	}
	if question == "" {
		fmt.Fprintln(stderr, "vox ask: a question is required (argv or stdin)")
		return 64
	}

	engine, err := buildEngine(getenv, *toolsPath, *allowWrite, *voiceOn, *replay)
	if err != nil {
		fmt.Fprintf(stderr, "vox ask: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	res, err := engine.Ask(ctx, question)
	if err != nil {
		fmt.Fprintf(stderr, "vox ask: %v\n", err)
		return 1
	}
	printResult(stdout, res, *asJSON)
	return exitCode(res.Kind)
}

func runDecide(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("decide", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	choices := fs.String("choices", "", "comma-separated options; the winner's text comes back")
	state := fs.String("state", "", "context the model may read (never a transcript)")
	instructions := fs.String("instructions", "", "override the default instruction line")
	timeout := fs.Duration("timeout", 45*time.Second, "whole-call timeout")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "vox decide: %v\n", err)
		return 64
	}
	question := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if question == "" {
		raw, _ := io.ReadAll(stdin)
		question = strings.TrimSpace(string(raw))
	}
	if question == "" || strings.TrimSpace(*choices) == "" {
		fmt.Fprintln(stderr, "vox decide: a question and -choices are required")
		return 64
	}
	decision := buildDecision(getenv)
	if decision == nil {
		fmt.Fprint(stderr, "vox decide: no decision model found (set VOX_JEV or put jev-decide on PATH)\n")
		return 1
	}

	var cs []vox.Choice
	for i, part := range strings.Split(*choices, ",") {
		text := strings.TrimSpace(part)
		if text == "" {
			continue
		}
		cs = append(cs, vox.Choice{Label: fmt.Sprintf("c%d", i), Text: text})
	}
	instructionsText := strings.TrimSpace(*instructions)
	if instructionsText == "" {
		instructionsText = "Pick the option that answers the question most accurately, concretely and without invented facts."
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	verdict, err := decision.Choose(ctx, vox.LaneRequest{
		State:        strings.TrimSpace(*state),
		Question:     question,
		Instructions: instructionsText,
		Choices:      cs,
	})
	if err != nil {
		fmt.Fprintf(stderr, "vox decide: %v\n", err)
		return 1
	}
	printVerdict(stdout, verdict)
	if verdict.Selected() {
		return 0
	}
	return 2
}

func runSay(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("say", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	timeout := fs.Duration("timeout", 90*time.Second, "whole-call timeout")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "vox say: %v\n", err)
		return 64
	}
	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if text == "" {
		raw, _ := io.ReadAll(stdin)
		text = strings.TrimSpace(string(raw))
	}
	if text == "" {
		fmt.Fprintln(stderr, "vox say: text is required (argv or stdin)")
		return 64
	}
	voice := buildVoice(getenv, true)
	if voice == nil {
		fmt.Fprint(stderr, "vox say: no chat lane found (set VOX_CPG or put cpg on PATH)\n")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	out, err := voice.Say(ctx, text)
	if err != nil {
		fmt.Fprintf(stderr, "vox say: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, out)
	return 0
}

func runTools(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("tools", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	toolsPath := fs.String("tools", "", "tool manifest (default $VOX_TOOLS or ./tools.json)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "vox tools: %v\n", err)
		return 64
	}
	path := manifestPath(getenv, *toolsPath)
	m, err := vox.LoadManifest(path)
	if err != nil {
		fmt.Fprintf(stderr, "vox tools: %v\n", err)
		return 1
	}
	for _, t := range m.Tools {
		kind := "read"
		if !t.ReadOnly {
			kind = "write"
		}
		fmt.Fprintf(stdout, "%-18s %-5s %s\n", t.Name, kind, strings.Join(t.Command, " "))
	}
	return 0
}

type doctorReport struct {
	Version          string   `json:"version"`
	Platform         string   `json:"platform"`
	Manifest         string   `json:"manifest"`
	Tools            int      `json:"tools"`
	WritableTools    int      `json:"writable_tools"`
	Caller           string   `json:"caller"`
	NeedleEngine     string   `json:"needle_engine,omitempty"`
	NeedleWeights    string   `json:"needle_weights,omitempty"`
	Decision         string   `json:"decision"`
	DecisionProvider string   `json:"decision_provider,omitempty"`
	Voice            string   `json:"voice"`
	VoiceModel       string   `json:"voice_model,omitempty"`
	Notes            []string `json:"notes,omitempty"`
}

func runDoctor(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "print the report as JSON")
	toolsPath := fs.String("tools", "", "tool manifest (default $VOX_TOOLS or ./tools.json)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "vox doctor: %v\n", err)
		return 64
	}

	report := doctorReport{Version: releaseVersion, Platform: runtime.GOOS + "/" + runtime.GOARCH}
	report.Manifest = manifestPath(getenv, *toolsPath)
	if m, err := vox.LoadManifest(report.Manifest); err != nil {
		report.Notes = append(report.Notes, "manifest: "+err.Error())
	} else {
		report.Tools = len(m.Tools)
		report.WritableTools = m.Writable()
	}

	caller := buildCaller(getenv, "")
	switch typed := caller.(type) {
	case vox.NeedleCaller:
		report.Caller = typed.Name()
		report.NeedleEngine, report.NeedleWeights = typed.Engine, typed.Weights
	default:
		report.Caller = caller.Name()
	}
	if nc, ok := caller.(vox.NeedleCaller); ok && (nc.Engine == "" || nc.Weights == "") {
		report.Notes = append(report.Notes, "no needle3 engine or weights: pass -replay <file>, or set VOX_NEEDLE_ENGINE and VOX_NEEDLE_WEIGHTS")
	}
	if runtime.GOARCH != "arm64" {
		report.Notes = append(report.Notes, "needle3's shipped engine is linux-arm64; on this platform the caller is a replay fixture or a chat model")
	}

	decision := buildDecision(getenv)
	if decision == nil {
		report.Decision = "missing"
		report.Notes = append(report.Notes, "no decision model: routing falls back to the caller's own pick")
	} else {
		report.Decision = decision.Name()
		if jev, ok := decision.(vox.JevDecision); ok {
			report.DecisionProvider = jev.Provider
			if jev.Provider == "" {
				report.DecisionProvider = "typesafe (default)"
			}
		}
	}

	voice := buildVoice(getenv, true)
	if voice == nil {
		report.Voice = "missing"
		report.Notes = append(report.Notes, "no chat lane: results come back as raw output with no explanation")
	} else {
		report.Voice = voice.Name()
		if cpg, ok := voice.(vox.CPGVoice); ok {
			report.VoiceModel = cpg.Model
			if cpg.Model == "" {
				report.VoiceModel = vox.DefaultVoiceModel
			}
		}
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return 0
	}
	fmt.Fprintf(stdout, "vox        %s (%s)\n", report.Version, report.Platform)
	fmt.Fprintf(stdout, "manifest   %s (%d tools, %d write)\n", report.Manifest, report.Tools, report.WritableTools)
	fmt.Fprintf(stdout, "caller     %s\n", report.Caller)
	fmt.Fprintf(stdout, "decision   %s %s\n", report.Decision, report.DecisionProvider)
	fmt.Fprintf(stdout, "voice      %s\n", report.Voice)
	for _, n := range report.Notes {
		fmt.Fprintf(stdout, "note       %s\n", n)
	}
	return 0
}

func printResult(w io.Writer, res vox.Result, asJSON bool) {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
		return
	}
	fmt.Fprintf(w, "decision   %s\n", res.Kind)
	if res.Lane != "" {
		fmt.Fprintf(w, "lane       %s (p=%.2f m=%.2f)\n", res.Lane, res.Probability, res.Margin)
	}
	if res.Tool != "" {
		fmt.Fprintf(w, "tool       %s\n", res.Tool)
	}
	if len(res.Args) > 0 {
		keys := make([]string, 0, len(res.Args))
		for k := range res.Args {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		pairs := make([]string, 0, len(keys))
		for _, k := range keys {
			pairs = append(pairs, k+"="+res.Args[k])
		}
		fmt.Fprintf(w, "args       %s\n", strings.Join(pairs, " "))
	}
	if res.Output != "" {
		fmt.Fprintf(w, "output     %s\n", firstLine(res.Output))
	}
	if res.Reason != "" {
		fmt.Fprintf(w, "reason     %s\n", res.Reason)
	}
	if res.Explanation != "" {
		fmt.Fprintf(w, "\n%s\n", res.Explanation)
	}
}

func printVerdict(w io.Writer, v vox.Verdict) {
	body, _ := json.Marshal(struct {
		Status      string  `json:"status"`
		Pick        string  `json:"pick"`
		Probability float64 `json:"probability"`
		Margin      float64 `json:"margin"`
	}{v.Status, v.Pick, v.Probability, v.Margin})
	fmt.Fprintln(w, string(body))
}

func exitCode(kind string) int {
	switch kind {
	case vox.KindAnswered, vox.KindActed:
		return 0
	case vox.KindConfirm:
		return 2
	case vox.KindDeclined:
		return 3
	case vox.KindEscalate:
		return 4
	default:
		return 1
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

func buildEngine(getenv func(string) string, toolsPath string, allowWrite, voiceOn bool, replay string) (vox.Engine, error) {
	m, err := vox.LoadManifest(manifestPath(getenv, toolsPath))
	if err != nil {
		return vox.Engine{}, err
	}
	minProb, _ := strconv.ParseFloat(envOr(getenv, "VOX_MIN_PROB"), 64)
	minMargin, _ := strconv.ParseFloat(envOr(getenv, "VOX_MIN_MARGIN"), 64)
	return vox.Engine{
		Manifest:   m,
		Caller:     buildCaller(getenv, replay),
		Decision:   buildDecision(getenv),
		Voice:      buildVoice(getenv, voiceOn),
		AllowWrite: allowWrite,
		MinProb:    minProb,
		MinMargin:  minMargin,
	}, nil
}

func manifestPath(getenv func(string) string, flagPath string) string {
	if p := strings.TrimSpace(flagPath); p != "" {
		return p
	}
	if p := envOr(getenv, "VOX_TOOLS"); p != "" {
		return p
	}
	return "tools.json"
}

func buildCaller(getenv func(string) string, replay string) vox.Caller {
	if strings.TrimSpace(replay) != "" {
		return vox.ReplayCaller{Path: replay}
	}
	engine := envOr(getenv, "VOX_NEEDLE_ENGINE", "NEEDLE_ENGINE")
	if engine == "" {
		engine = lookPath("needle")
	}
	weights := envOr(getenv, "VOX_NEEDLE_WEIGHTS", "NEEDLE_WEIGHTS")
	return vox.NeedleCaller{Engine: engine, Weights: weights}
}

func buildDecision(getenv func(string) string) vox.Decision {
	bin := envOr(getenv, "VOX_JEV")
	if bin == "" {
		bin = lookPath("jev-decide")
	}
	if bin == "" {
		return nil
	}
	return vox.JevDecision{Bin: bin, Provider: envOr(getenv, "VOX_JEV_PROVIDER")}
}

func buildVoice(getenv func(string) string, on bool) vox.Voice {
	if !on {
		return nil
	}
	bin := envOr(getenv, "VOX_CPG")
	if bin == "" {
		bin = lookPath("cpg")
	}
	if bin == "" {
		return nil
	}
	return vox.CPGVoice{
		Bin:     bin,
		Model:   envOr(getenv, "VOX_CPG_MODEL"),
		Session: envOr(getenv, "VOX_SESSION"),
	}
}

func envOr(getenv func(string) string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func lookPath(name string) string {
	if name == "" {
		return ""
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

func usage() string {
	return `vox ` + releaseVersion + `

The explainer layer: a small caller picks one tool, a decision model routes it,
and a chat model says the result to a person.

Usage:
  vox ask [-json] [-allow-write] [-voice=false] [-replay FILE] [-tools FILE] <question>
  vox decide -choices "a,b,c" [-state TEXT] <question>
  vox say <text>
  vox tools [-tools FILE]
  vox doctor [-json]
  vox version

Exit codes:
  0  answered or acted        2  confirm (below the gate, or a write tool)
  3  declined                 4  escalate (a reasoning model's job)

Environment:
  VOX_TOOLS            tool manifest (default ./tools.json)
  VOX_NEEDLE_ENGINE    needle3 engine (fallback NEEDLE_ENGINE)
  VOX_NEEDLE_WEIGHTS   needle3 weights file (fallback NEEDLE_WEIGHTS)
  VOX_JEV              decision client (default: jev-decide on PATH)
  VOX_JEV_PROVIDER     provider named on every call (default typesafe)
  VOX_CPG              chat client (default: cpg on PATH)
  VOX_CPG_MODEL        chat model (default ` + vox.DefaultVoiceModel + `)
  VOX_SESSION          chat session label (default ` + vox.DefaultVoiceSession + `)
  VOX_MIN_PROB         gate: minimum probability (default 0.8)
  VOX_MIN_MARGIN       gate: minimum margin (default 0.15)

Examples:
  vox doctor -json
  vox ask "what role is fern"
  vox ask -replay testdata/role-call.json "what role is fern"
  vox decide -choices "run the lookup,ask a person" "is this answer worth a tool call"
`
}
