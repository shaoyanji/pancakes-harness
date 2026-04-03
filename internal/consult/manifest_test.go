package consult

import (
	"errors"
	"reflect"
	"testing"
)

func TestGenerateDeterministicManifest(t *testing.T) {
	t.Parallel()

	in := Input{
		SessionID:   "s1",
		BranchID:    "main",
		Fingerprint: "fp-1",
		Mode:        "agent_call",
		Scope:       "branch:main",
		Refs:        []string{"r2", "r1"},
		Constraints: map[string]string{
			"b": "2",
			"a": "1",
		},
		SelectedItems: []SelectedItem{
			{ID: "item-b", Kind: "tool.result", Ref: "ref://b", Bytes: 10},
			{ID: "item-a", Kind: "turn.user", Ref: "ref://a", Bytes: 2},
		},
		ByteBudget:  512,
		Compacted:   false,
		Truncated:   false,
		TaskSummary: "summarize",
	}

	m1, err := Generate(in)
	if err != nil {
		t.Fatalf("generate 1: %v", err)
	}
	m2, err := Generate(in)
	if err != nil {
		t.Fatalf("generate 2: %v", err)
	}
	j1, err := Marshal(m1)
	if err != nil {
		t.Fatalf("marshal 1: %v", err)
	}
	j2, err := Marshal(m2)
	if err != nil {
		t.Fatalf("marshal 2: %v", err)
	}
	if string(j1) != string(j2) {
		t.Fatalf("expected deterministic manifest output, got %s vs %s", string(j1), string(j2))
	}
}

func TestGenerateStableOrderingRefsConstraintsSelectedItems(t *testing.T) {
	t.Parallel()

	in := Input{
		SessionID:   "s-order",
		BranchID:    "main",
		Fingerprint: "fp-order",
		Mode:        "agent_call",
		Scope:       "branch:main",
		Refs:        []string{"z-ref", "a-ref", "m-ref"},
		Constraints: map[string]string{
			"z": "last",
			"a": "first",
		},
		SelectedItems: []SelectedItem{
			{ID: "item-3", Kind: "summary", Ref: "r3", Bytes: 3},
			{ID: "item-1", Kind: "turn", Ref: "r1", Bytes: 1},
			{ID: "item-2", Kind: "tool", Ref: "r2", Bytes: 2},
		},
	}

	m, err := Generate(in)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !reflect.DeepEqual(m.Refs, []string{"a-ref", "m-ref", "z-ref"}) {
		t.Fatalf("unexpected ref order: %#v", m.Refs)
	}
	if got := []string{
		m.SelectedItems[0].ID,
		m.SelectedItems[1].ID,
		m.SelectedItems[2].ID,
	}; !reflect.DeepEqual(got, []string{"item-1", "item-2", "item-3"}) {
		t.Fatalf("unexpected selected item order: %#v", got)
	}
}

func TestGenerateByteAccountingConsistent(t *testing.T) {
	t.Parallel()

	m, err := Generate(Input{
		SessionID:   "s-bytes",
		BranchID:    "main",
		Fingerprint: "fp-bytes",
		Mode:        "agent_call",
		Scope:       "branch:main",
		Refs:        []string{"ref-1"},
		ByteBudget:  10,
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	b, err := Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if m.ActualBytes <= 0 {
		t.Fatalf("expected populated actual bytes, got %d", m.ActualBytes)
	}
	if m.ActualBytes != len(b) {
		t.Fatalf("expected actual bytes to equal marshaled length, got %d vs %d", m.ActualBytes, len(b))
	}
	if m.ByteBudget != 10 {
		t.Fatalf("expected byte budget to be preserved, got %d", m.ByteBudget)
	}
	if !m.Compacted {
		t.Fatalf("expected compacted=true when actual bytes exceed budget; manifest=%#v", m)
	}
}

func TestGenerateEquivalentNormalizedInputsProduceIdenticalManifests(t *testing.T) {
	t.Parallel()

	inA := Input{
		SessionID:   " s-eq ",
		BranchID:    " main ",
		Fingerprint: " fp-eq ",
		Mode:        " agent_call ",
		Scope:       " branch:main ",
		Refs:        []string{"r2", " r1 "},
		Constraints: map[string]string{
			"b": "2",
			"a": " 1 ",
		},
		SelectedItems: []SelectedItem{
			{ID: " item-b ", Kind: " tool ", Ref: " rb ", Bytes: 2},
			{ID: " item-a ", Kind: " turn ", Ref: " ra ", Bytes: 1},
		},
		TaskSummary: " summarize ",
	}
	inB := Input{
		SessionID:   "s-eq",
		BranchID:    "main",
		Fingerprint: "fp-eq",
		Mode:        "agent_call",
		Scope:       "branch:main",
		Refs:        []string{"r1", "r2"},
		Constraints: map[string]string{
			"a": "1",
			"b": "2",
		},
		SelectedItems: []SelectedItem{
			{ID: "item-a", Kind: "turn", Ref: "ra", Bytes: 1},
			{ID: "item-b", Kind: "tool", Ref: "rb", Bytes: 2},
		},
		TaskSummary: "summarize",
	}

	mA, err := Generate(inA)
	if err != nil {
		t.Fatalf("generate A: %v", err)
	}
	mB, err := Generate(inB)
	if err != nil {
		t.Fatalf("generate B: %v", err)
	}
	jA, err := Marshal(mA)
	if err != nil {
		t.Fatalf("marshal A: %v", err)
	}
	jB, err := Marshal(mB)
	if err != nil {
		t.Fatalf("marshal B: %v", err)
	}
	if string(jA) != string(jB) {
		t.Fatalf("expected equivalent normalized inputs to match, got %s vs %s", string(jA), string(jB))
	}
}

func TestGenerateNormalizesSelectionExplanationDeterministically(t *testing.T) {
	t.Parallel()

	m, err := Generate(Input{
		SessionID:   "s-select",
		BranchID:    "main",
		Fingerprint: "fp-select",
		Mode:        "agent_call",
		Scope:       "branch:main",
		SelectedItems: []SelectedItem{
			{ID: "item-b", Kind: "tool.result", SummaryRef: "summary://tool/1", Bytes: 4, Reason: "tool_result"},
			{ID: "item-a", Kind: "turn.user", Bytes: 2, Reason: "recent_turn"},
		},
		Selection: &SelectionExplanation{
			Included: []SelectionItem{
				{ID: "item-b", Kind: "tool.result", Reason: "tool_result", Class: "summary_only"},
				{ID: "item-a", Kind: "turn.user", Reason: "recent_turn", Class: "passthrough"},
			},
			Excluded: []SelectionItem{
				{ID: "sib", Kind: "turn.agent", Reason: "non_local", Class: "drop_unless_asked"},
				{ID: "dbg", Kind: "packet.sent", Reason: "debug_never", Class: "debug_never"},
			},
			DominantInclusionReasons: []ReasonCount{
				{Reason: "recent_turn", Count: 1},
				{Reason: "tool_result", Count: 1},
			},
			DominantExclusionReasons: []ReasonCount{
				{Reason: "non_local", Count: 3},
				{Reason: "debug_never", Count: 1},
			},
			BudgetPressure: true,
		},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got := []string{m.SelectedItems[0].ID, m.SelectedItems[1].ID}; !reflect.DeepEqual(got, []string{"item-a", "item-b"}) {
		t.Fatalf("unexpected selected item order: %#v", got)
	}
	if m.SelectedItems[0].Reason != "recent_turn" || m.SelectedItems[1].Reason != "tool_result" {
		t.Fatalf("unexpected selected item reasons: %#v", m.SelectedItems)
	}
	if m.Selection == nil || !m.Selection.BudgetPressure {
		t.Fatalf("expected selection explanation with budget pressure, got %#v", m.Selection)
	}
	if got := []string{m.Selection.Excluded[0].ID, m.Selection.Excluded[1].ID}; !reflect.DeepEqual(got, []string{"dbg", "sib"}) {
		t.Fatalf("unexpected excluded order: %#v", m.Selection.Excluded)
	}
	if got := m.Selection.DominantExclusionReasons; len(got) != 2 || got[0].Reason != "non_local" || got[0].Count != 3 || got[1].Reason != "debug_never" {
		t.Fatalf("unexpected dominant exclusion reasons: %#v", got)
	}
}

func TestGenerateMalformedInput(t *testing.T) {
	t.Parallel()

	_, err := Generate(Input{
		SessionID:   "s",
		BranchID:    "main",
		Fingerprint: "",
		Mode:        "agent_call",
	})
	if !errors.Is(err, ErrMalformedInput) {
		t.Fatalf("expected malformed error, got %v", err)
	}
}
