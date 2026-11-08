package vox_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"pancakes-harness/internal/vox"
)

// scriptedCaller answers in a fixed order, so a test states the caller's
// behaviour instead of inferring it from a manifest.
type scriptedCaller struct {
	replies []func() (vox.Call, bool, error)
	i       int
}

func (s *scriptedCaller) Name() string { return "scripted" }

func (s *scriptedCaller) Call(context.Context, string, vox.Manifest) (vox.Call, bool, error) {
	reply := s.replies[s.i%len(s.replies)]
	s.i++
	return reply()
}

func batteryManifest() vox.Manifest {
	return vox.Manifest{Tools: []vox.Tool{
		{Name: "host_fact", Parameters: json.RawMessage(`{"required":["host","field"]}`), Command: []string{"true"}, ReadOnly: true},
		{Name: "notes_search", Parameters: json.RawMessage(`{"required":["q"]}`), Command: []string{"true"}, ReadOnly: true},
	}}
}

func called(tool string, conf float64, args map[string]string) func() (vox.Call, bool, error) {
	return func() (vox.Call, bool, error) {
		return vox.Call{Tool: tool, Args: args, Confidence: conf, Calls: 1}, true, nil
	}
}

func nothing() (vox.Call, bool, error) { return vox.Call{}, false, nil }

func TestBatteryScoresToolArgsAndSilence(t *testing.T) {
	caller := &scriptedCaller{replies: []func() (vox.Call, bool, error){
		// The right tool with a capitalised host: a spelling difference is not
		// a wrong machine, so this counts as correct.
		called("host_fact", 0.8, map[string]string{"host": "Frieren", "field": "role"}),
		called("host_fact", 0.4, nil),
		nothing,
		called("notes_search", 0.2, map[string]string{"q": "anything"}),
	}}
	cases := []vox.Case{
		{ID: "role", Prompt: "what role is frieren", Tool: "host_fact", Args: map[string]string{"host": "frieren"}},
		{ID: "notes", Prompt: "search my notes for sops", Tool: "notes_search"},
		{ID: "arithmetic", Prompt: "what is 2 plus 2", Tool: ""},
		{ID: "greeting", Prompt: "say hello", Tool: ""},
	}
	sum, err := vox.RunBattery(context.Background(), caller, batteryManifest(), cases, 1)
	if err != nil {
		t.Fatalf("battery: %v", err)
	}
	if sum.Attempts != 4 || sum.Correct != 2 {
		t.Fatalf("want 2/4 correct, got %d/%d", sum.Correct, sum.Attempts)
	}
	if sum.WrongTool != 2 {
		t.Fatalf("want 2 wrong-tool attempts, got %d", sum.WrongTool)
	}
	if sum.ArgsChecked != 1 || sum.ArgsCorrect != 1 {
		t.Fatalf("want 1/1 args, got %d/%d", sum.ArgsCorrect, sum.ArgsChecked)
	}
	if got := sum.PerTool["(no call)"]; got.Want != 2 || got.Got != 1 {
		t.Fatalf("silence was not scored per case: %+v", got)
	}
	if sum.Accuracy() != 0.5 {
		t.Fatalf("want accuracy 0.5, got %v", sum.Accuracy())
	}
}

func TestBatteryFindsAFlakyCase(t *testing.T) {
	caller := &scriptedCaller{replies: []func() (vox.Call, bool, error){
		called("host_fact", 0.5, nil),
		called("notes_search", 0.5, nil),
	}}
	cases := []vox.Case{{ID: "unstable", Prompt: "which host", Tool: "host_fact"}}
	sum, err := vox.RunBattery(context.Background(), caller, batteryManifest(), cases, 2)
	if err != nil {
		t.Fatalf("battery: %v", err)
	}
	if sum.Attempts != 2 || sum.Correct != 1 || sum.WrongTool != 1 {
		t.Fatalf("repeats were not scored separately: %+v", sum)
	}
	if len(sum.Flaky) != 1 || sum.Flaky[0] != "unstable" {
		t.Fatalf("a disagreeing case was not reported: %v", sum.Flaky)
	}
}

func TestBatteryRejectsDriftedCases(t *testing.T) {
	cases := []vox.Case{{ID: "gone", Prompt: "delete everything", Tool: "rm_rf"}}
	_, err := vox.RunBattery(context.Background(), &scriptedCaller{replies: []func() (vox.Call, bool, error){nothing}}, batteryManifest(), cases, 1)
	if !errors.Is(err, vox.ErrUnknownTool) {
		t.Fatalf("a case naming an undeclared tool must fail the battery, got %v", err)
	}
}

func TestBatteryNeedsACaller(t *testing.T) {
	_, err := vox.RunBattery(context.Background(), nil, batteryManifest(), []vox.Case{{ID: "x", Prompt: "y"}}, 1)
	if !errors.Is(err, vox.ErrNoCaller) {
		t.Fatalf("want ErrNoCaller, got %v", err)
	}
}

func TestLoadCasesNamesAndValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "battery.json")
	body := `{"cases":[{"prompt":"what is frieren"},{"id":"named","prompt":"say hello","tool":""}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write cases: %v", err)
	}
	cases, err := vox.LoadCases(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cases[0].ID != "case-1" || cases[1].ID != "named" {
		t.Fatalf("ids were not assigned: %+v", cases)
	}

	dup := filepath.Join(t.TempDir(), "dup.json")
	if err := os.WriteFile(dup, []byte(`{"cases":[{"id":"a","prompt":"x"},{"id":"a","prompt":"y"}]}`), 0o600); err != nil {
		t.Fatalf("write dup: %v", err)
	}
	if _, err := vox.LoadCases(dup); err == nil {
		t.Fatal("a duplicate case id was accepted")
	}
}
