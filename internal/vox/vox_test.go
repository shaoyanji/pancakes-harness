package vox_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pancakes-harness/internal/vox"
)

func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake.sh")
	content := "#!/bin/sh\n" + body
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	return path
}

func roleManifest() vox.Manifest {
	return vox.Manifest{Tools: []vox.Tool{{
		Name:        "host_fact",
		Description: "one registry field for a named machine",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"host":{"type":"string"},"field":{"type":"string"}},"required":["host","field"]}`),
		Command:     []string{"printf", "%s=%s", "{host}", "{field}"},
		ReadOnly:    true,
	}}}
}

func replayFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "call.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write replay: %v", err)
	}
	return path
}

func TestParseCallRefusesBeforeConfidence(t *testing.T) {
	m := roleManifest()
	cases := []struct {
		name string
		body string
		want error
	}{
		{"invented tool", `{"function_calls":[{"name":"rm_rf","arguments":{}}],"confidence":0.99}`, vox.ErrUnknownTool},
		{"ungrounded", `{"function_calls":[{"name":"host_fact","arguments":{"host":"fern","field":"role"}}],"validation":{"ungrounded":["host"]}}`, vox.ErrUngrounded},
		{"missing argument", `{"function_calls":[{"name":"host_fact","arguments":{"host":"fern"}}],"confidence":0.9}`, vox.ErrMissingArg},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok, err := vox.ParseCall([]byte(tc.body), m)
			if ok {
				t.Fatal("the call was accepted")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestParseCallAcceptsAndCounts(t *testing.T) {
	body := `{"function_calls":[{"name":"host_fact","arguments":{"host":"fern","field":"role"}},{"name":"host_fact","arguments":{"host":"eisen","field":"role"}}],"confidence":0.71}`
	call, ok, err := vox.ParseCall([]byte(body), roleManifest())
	if err != nil || !ok {
		t.Fatalf("call rejected: %v", err)
	}
	if call.Calls != 2 {
		t.Fatalf("want 2 calls, got %d", call.Calls)
	}
	if call.Confidence != 0.71 {
		t.Fatalf("confidence not carried: %v", call.Confidence)
	}
}

func TestNeedleCallerSendsSchemaOnly(t *testing.T) {
	copied := filepath.Join(t.TempDir(), "sent.json")
	engine := script(t, `cp "$4" "`+copied+`"
printf '%s' '{"function_calls":[{"name":"host_fact","arguments":{"host":"fern","field":"role"}}],"confidence":0.82}'`)

	caller := vox.NeedleCaller{Engine: engine, Weights: filepath.Join(t.TempDir(), "w.cact")}
	call, ok, err := caller.Call(context.Background(), "what role is fern", roleManifest())
	if err != nil || !ok {
		t.Fatalf("call failed: ok=%v err=%v", ok, err)
	}
	if call.Tool != "host_fact" || call.Args["host"] != "fern" {
		t.Fatalf("unexpected call: %+v", call)
	}

	raw, err := os.ReadFile(copied)
	if err != nil {
		t.Fatalf("engine never received the surface: %v", err)
	}
	if strings.Contains(string(raw), "command") || strings.Contains(string(raw), "read_only") {
		t.Fatalf("the surface leaked harness fields to the caller: %s", raw)
	}
}

func TestNeedleCallerWithoutEngine(t *testing.T) {
	_, _, err := vox.NeedleCaller{}.Call(context.Background(), "anything", roleManifest())
	if !errors.Is(err, vox.ErrNoCaller) {
		t.Fatalf("want ErrNoCaller, got %v", err)
	}
}

func TestJevDecisionReadsNeedsReview(t *testing.T) {
	bin := script(t, `printf '%s' '{"decisions":{"lane":{"status":"needs_review","value":"c0","probability":0.62,"margin":0.03}}}'
exit 2`)
	verdict, err := vox.JevDecision{Bin: bin}.Choose(context.Background(), vox.LaneRequest{
		Question: "run the lookup?",
		Choices:  []vox.Choice{{Label: "c0", Text: "run the lookup"}},
	})
	if err != nil {
		t.Fatalf("a needs_review exit is an answer, not an error: %v", err)
	}
	if verdict.Selected() {
		t.Fatalf("needs_review was reported as selected: %+v", verdict)
	}
	if verdict.Pick != "c0" {
		t.Fatalf("the pick was lost: %+v", verdict)
	}
}

func TestJevDecisionFailsClosed(t *testing.T) {
	bin := script(t, "exit 1")
	if _, err := (vox.JevDecision{Bin: bin}).Choose(context.Background(), vox.LaneRequest{Question: "x"}); err == nil {
		t.Fatal("an error exit was reported as a decision")
	}
}

func TestAskActsAndExplains(t *testing.T) {
	engine := vox.Engine{
		Manifest:  roleManifest(),
		Caller:    vox.ReplayCaller{Path: replayFile(t, `{"function_calls":[{"name":"host_fact","arguments":{"host":"fern","field":"role"}}],"confidence":0.9}`)},
		Decision:  vox.FixedDecision{Label: "c0", Probability: 0.93, Margin: 0.51},
		Voice:     vox.StaticVoice{Text: "fern is a NixOS host."},
		MinProb:   0.8,
		MinMargin: 0.15,
	}
	res, err := engine.Ask(context.Background(), "what role is fern")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.Kind != vox.KindActed {
		t.Fatalf("want acted, got %s (%s)", res.Kind, res.Reason)
	}
	if res.Output != "fern=role" {
		t.Fatalf("unexpected tool output: %q", res.Output)
	}
	if res.Explanation != "fern is a NixOS host." {
		t.Fatalf("the voice did not run: %q", res.Explanation)
	}
}

func TestAskGatesAWeakDecision(t *testing.T) {
	engine := vox.Engine{
		Manifest:  roleManifest(),
		Caller:    vox.ReplayCaller{Path: replayFile(t, `{"function_calls":[{"name":"host_fact","arguments":{"host":"fern","field":"role"}}],"confidence":0.9}`)},
		Decision:  vox.FixedDecision{Label: "c0", Probability: 0.62, Margin: 0.02},
		MinProb:   0.8,
		MinMargin: 0.15,
	}
	res, err := engine.Ask(context.Background(), "what role is fern")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.Kind != vox.KindConfirm {
		t.Fatalf("a decision below the gate must be a proposal, got %s", res.Kind)
	}
	if res.Output != "" {
		t.Fatal("the tool ran anyway")
	}
}

func TestAskRefusesAWriteToolWithoutAllow(t *testing.T) {
	m := roleManifest()
	m.Tools[0].ReadOnly = false
	engine := vox.Engine{
		Manifest: m,
		Caller:   vox.ReplayCaller{Path: replayFile(t, `{"function_calls":[{"name":"host_fact","arguments":{"host":"fern","field":"role"}}],"confidence":0.9}`)},
		Decision: vox.FixedDecision{Label: "c0", Probability: 0.95, Margin: 0.9},
	}
	res, err := engine.Ask(context.Background(), "what role is fern")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.Kind != vox.KindConfirm {
		t.Fatalf("want confirm, got %s", res.Kind)
	}
	if res.Output != "" {
		t.Fatal("a write tool ran without an explicit allow")
	}
}

func TestAskEscalatesAReducibleRequest(t *testing.T) {
	engine := vox.Engine{
		Manifest: roleManifest(),
		Caller:   vox.ReplayCaller{Path: replayFile(t, `{"function_calls":[{"name":"host_fact","arguments":{"host":"fern","field":"role"}},{"name":"host_fact","arguments":{"host":"eisen","field":"role"}}]}`)},
		Decision: vox.FixedDecision{Label: "c0", Probability: 0.95, Margin: 0.9},
	}
	res, err := engine.Ask(context.Background(), "compare fern and eisen")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.Kind != vox.KindEscalate {
		t.Fatalf("want escalate, got %s", res.Kind)
	}
}

func TestAskDeclinesAnUnknownTool(t *testing.T) {
	engine := vox.Engine{
		Manifest: roleManifest(),
		Caller:   vox.ReplayCaller{Path: replayFile(t, `{"function_calls":[{"name":"shell","arguments":{"cmd":"rm -rf /"}}]}`)},
		Decision: vox.FixedDecision{Label: "c0", Probability: 1, Margin: 1},
	}
	res, err := engine.Ask(context.Background(), "delete the tree")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.Kind != vox.KindDeclined {
		t.Fatalf("want declined, got %s", res.Kind)
	}
}

func TestLoadManifestRejectsDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tools.json")
	body := `[{"name":"a","command":["true"],"read_only":true},{"name":"a","command":["true"],"read_only":true}]`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if _, err := vox.LoadManifest(path); err == nil {
		t.Fatal("a duplicate tool name was accepted")
	}
}

func TestExplainPromptBoundsAndForbidsInvention(t *testing.T) {
	long := strings.Repeat("x", vox.MaxExplainChars+500)
	prompt := vox.ExplainPrompt("what is it", "host_fact", map[string]string{"host": "fern"}, long)
	if !strings.Contains(prompt, "Do not add facts") {
		t.Fatal("the prompt does not forbid invention")
	}
	if len([]rune(prompt)) > vox.MaxExplainChars+600 {
		t.Fatalf("the packet was not bounded: %d runes", len([]rune(prompt)))
	}
}

// TestRecordedEngineOutputParses keeps the shape honest against the binary that
// produces it: these two files are verbatim needle3 responses, captured on
// delphi (aarch64) 2026-11-08 from the engine the fleet pins, not hand-written.
// The engine carries fields the harness ignores (type, success, reasoning,
// suppressed_calls, prefill_tps) which is the point — a caller that fails to
// parse a superset would look like a caller that found no tool.
func TestRecordedEngineOutputParses(t *testing.T) {
	m := vox.Manifest{Tools: []vox.Tool{
		{Name: "host_name", Command: []string{"hostname"}, ReadOnly: true},
		{Name: "disk_free", Command: []string{"df", "-h", "/"}, ReadOnly: true},
		{Name: "kernel_version", Command: []string{"uname", "-r"}, ReadOnly: true},
	}}
	cases := []struct {
		file string
		tool string
	}{
		{"needle-hostname.json", "host_name"},
		{"needle-disk.json", "disk_free"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			call, ok, err := vox.ParseCall(raw, m)
			if err != nil {
				t.Fatalf("a recorded engine response was rejected: %v", err)
			}
			if !ok {
				t.Fatal("a recorded call was read as no call")
			}
			if call.Tool != tc.tool {
				t.Fatalf("want %s, got %s", tc.tool, call.Tool)
			}
			if call.Confidence <= 0 || call.Confidence > 1 {
				t.Fatalf("confidence out of range: %v", call.Confidence)
			}
			if call.Calls != 1 {
				t.Fatalf("want one call, got %d", call.Calls)
			}
		})
	}
}

// TestRecordedRoundReplaysThroughTheLoop runs a captured round end to end, so
// the x86_64 targets exercise the same bytes the arm64 engine produced.
func TestRecordedRoundReplaysThroughTheLoop(t *testing.T) {
	engine := vox.Engine{
		Manifest: vox.Manifest{Tools: []vox.Tool{{
			Name:     "host_name",
			Command:  []string{"hostname"},
			ReadOnly: true,
		}}},
		Caller:   vox.ReplayCaller{Path: filepath.Join("testdata", "needle-hostname.json")},
		Decision: vox.FixedDecision{Label: "c0", Probability: 0.99, Margin: 0.9},
		Voice:    vox.StaticVoice{Text: "This machine is delphi."},
	}
	res, err := engine.Ask(context.Background(), "what is this machines hostname")
	if err != nil {
		t.Fatalf("ask: %v", err)
	}
	if res.Kind != vox.KindActed {
		t.Fatalf("want acted, got %s (%s)", res.Kind, res.Reason)
	}
	if res.Output == "" {
		t.Fatal("the tool produced no output")
	}
	if res.Explanation == "" {
		t.Fatal("the voice did not run")
	}
}

// TestAskWithoutAnEngineFallsBackToTheVoice is the x86_64 case: the engine ships
// arm64 only, so a host without one must still be able to answer — with a chat
// lane, and saying so, rather than failing the turn.
func TestAskWithoutAnEngineFallsBackToTheVoice(t *testing.T) {
	engine := vox.Engine{
		Manifest: roleManifest(),
		Caller:   vox.NeedleCaller{},
		Decision: vox.FixedDecision{Label: "c0", Probability: 0.99, Margin: 0.9},
		Voice:    vox.StaticVoice{Text: "answered without a lookup."},
	}
	res, err := engine.Ask(context.Background(), "what is nix")
	if err != nil {
		t.Fatalf("a missing engine must not be a failure: %v", err)
	}
	if res.Kind != vox.KindAnswered {
		t.Fatalf("want answered, got %s (%s)", res.Kind, res.Reason)
	}
	if res.Explanation == "" {
		t.Fatal("the voice did not run")
	}
	if !strings.Contains(res.Reason, "chat lane alone") {
		t.Fatalf("the fallback was not reported: %q", res.Reason)
	}
}
