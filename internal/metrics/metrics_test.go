package metrics

import "testing"

func TestSnapshotIncludesSelectorReasonCounts(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.IncSelectorInclusionReason("recent_turn", 2)
	reg.IncSelectorInclusionReason("summary_checkpoint", 1)
	reg.IncSelectorExclusionReason("non_local", 3)
	reg.IncSelectorBudgetPressure()

	snap := reg.Snapshot()
	if got := snap.SelectorInclusionReasonCounts["recent_turn"]; got != 2 {
		t.Fatalf("expected recent_turn=2, got %d", got)
	}
	if got := snap.SelectorExclusionReasonCounts["non_local"]; got != 3 {
		t.Fatalf("expected non_local=3, got %d", got)
	}
	if snap.SelectorBudgetPressureTotal != 1 {
		t.Fatalf("expected selector budget pressure total 1, got %d", snap.SelectorBudgetPressureTotal)
	}
}
