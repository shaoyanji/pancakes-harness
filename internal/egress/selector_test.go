package egress

import (
	"strings"
	"testing"
)

func TestDebugMetricsNeverEnterEgress(t *testing.T) {
	t.Parallel()

	in := []Candidate{
		{ID: "e1", Kind: "packet.sent", FrontierOrdinal: 0, IsActiveBranch: true},
		{ID: "e2", Kind: "metrics.snapshot", FrontierOrdinal: 1, IsActiveBranch: true},
		{ID: "e3", Kind: "trace.debug", FrontierOrdinal: 2, IsActiveBranch: true},
	}
	out := Select(in)
	for _, sel := range out {
		if sel.Include {
			t.Fatalf("expected debug/metrics items to be excluded, got %#v", sel)
		}
		if sel.Class != ClassDebugNever {
			t.Fatalf("expected class %q, got %q", ClassDebugNever, sel.Class)
		}
	}
}

func TestConsultEventsNeverEnterEgress(t *testing.T) {
	t.Parallel()

	in := []Candidate{
		{ID: "c1", Kind: "consult.resolved", FrontierOrdinal: 0, IsActiveBranch: true},
		{ID: "c2", Kind: "consult.unresolved", FrontierOrdinal: 1, IsActiveBranch: true},
	}
	out := Select(in)
	for _, sel := range out {
		if sel.Include {
			t.Fatalf("expected consult items to be excluded, got %#v", sel)
		}
		if sel.Class != ClassDebugNever {
			t.Fatalf("expected class %q, got %q", ClassDebugNever, sel.Class)
		}
	}
}

func TestLatestToolResultBecomesSummaryOnly(t *testing.T) {
	t.Parallel()

	out := Select([]Candidate{
		{ID: "u1", Kind: "turn.user", Text: "hello", FrontierOrdinal: 0, IsActiveBranch: true},
		{ID: "t1", Kind: "tool.result", Text: "huge payload", SummaryRef: "summary://tool/c1", FrontierOrdinal: 1, IsLatestToolResult: true, IsActiveBranch: true},
	})
	if len(out) != 2 {
		t.Fatalf("unexpected selection size: %d", len(out))
	}
	got := out[1]
	if !got.Include || got.Class != ClassSummaryOnly {
		t.Fatalf("expected summary_only include=true, got %#v", got)
	}
	if got.Text != "" || got.SummaryRef != "summary://tool/c1" {
		t.Fatalf("expected summary-only projection, got %#v", got)
	}
	if got.Reason != ReasonToolResult {
		t.Fatalf("expected tool_result reason, got %#v", got)
	}
}

func TestCurrentUserTurnRemainsPassthrough(t *testing.T) {
	t.Parallel()

	out := Select([]Candidate{
		{ID: "u1", Kind: "turn.user", Text: "latest ask", FrontierOrdinal: 3, IsCurrentUserTurn: true, IsActiveBranch: true},
	})
	if len(out) != 1 {
		t.Fatalf("unexpected size: %d", len(out))
	}
	if out[0].Class != ClassPassthrough || !out[0].Include || out[0].Text != "latest ask" {
		t.Fatalf("expected passthrough current user turn, got %#v", out[0])
	}
}

func TestStaleLargeItemsDowngradeToRefOnly(t *testing.T) {
	t.Parallel()

	large := strings.Repeat("x", 1200)
	out := Select([]Candidate{
		{ID: "old", Kind: "turn.agent", Text: large, BlobRef: "blob://old", FrontierOrdinal: 0, IsActiveBranch: true},
		{ID: "new", Kind: "turn.user", Text: "latest", FrontierOrdinal: 20, IsCurrentUserTurn: true, IsActiveBranch: true},
	})
	if len(out) != 2 {
		t.Fatalf("unexpected size: %d", len(out))
	}
	old := out[0]
	if old.Class != ClassRefOnly || !old.Include {
		t.Fatalf("expected ref_only include=true, got %#v", old)
	}
	if old.Text != "" || old.BlobRef != "blob://old" {
		t.Fatalf("expected ref-only body, got %#v", old)
	}
}

func TestSiblingBranchResidueDoesNotEnterByDefault(t *testing.T) {
	t.Parallel()

	out := Select([]Candidate{
		{ID: "a1", Kind: "turn.user", BranchID: "main", ActiveBranchID: "main", IsActiveBranch: true, Text: "active", FrontierOrdinal: 3, IsCurrentUserTurn: true},
		{ID: "s1", Kind: "turn.agent", BranchID: "alt", ActiveBranchID: "main", IsActiveBranch: false, Text: "sibling residue", FrontierOrdinal: 2},
	})
	if len(out) != 2 {
		t.Fatalf("unexpected size: %d", len(out))
	}
	if out[1].Include {
		t.Fatalf("expected sibling residue excluded, got %#v", out[1])
	}
	if out[1].Class != ClassDropUnlessAsked {
		t.Fatalf("expected class drop_unless_asked, got %q", out[1].Class)
	}
}

func TestActiveBranchHeadAndNearestSummaryAreSelected(t *testing.T) {
	t.Parallel()

	out := Select([]Candidate{
		{ID: "sum-old", Kind: "summary.checkpoint", IsActiveBranch: true, IsCheckpoint: true, SummaryRef: "summary://checkpoint/old", FrontierOrdinal: 1},
		{ID: "sum-near", Kind: "summary.checkpoint", IsActiveBranch: true, IsCheckpoint: true, IsNearestSummary: true, SummaryRef: "summary://checkpoint/new", FrontierOrdinal: 9},
		{ID: "head-user", Kind: "turn.user", IsActiveBranch: true, IsCurrentUserTurn: true, Text: "latest question", FrontierOrdinal: 10},
	})

	if out[1].Class != ClassSummaryOnly || !out[1].Include || out[1].SummaryRef != "summary://checkpoint/new" {
		t.Fatalf("expected nearest summary to be selected summary_only, got %#v", out[1])
	}
	if out[1].Reason != ReasonSummaryCheckpoint {
		t.Fatalf("expected summary_checkpoint reason, got %#v", out[1])
	}
	if out[2].Class != ClassPassthrough || !out[2].Include || out[2].Text != "latest question" {
		t.Fatalf("expected active head user passthrough, got %#v", out[2])
	}
	if out[2].Reason != ReasonRecentTurn {
		t.Fatalf("expected recent_turn reason, got %#v", out[2])
	}
}

func TestLatestActiveBranchToolSummaryIsPreserved(t *testing.T) {
	t.Parallel()

	out := Select([]Candidate{
		{ID: "tool-old", Kind: "tool.result", IsActiveBranch: true, SummaryRef: "summary://tool/old", FrontierOrdinal: 2},
		{ID: "tool-latest", Kind: "tool.result", IsActiveBranch: true, IsLatestToolResult: true, SummaryRef: "summary://tool/new", Text: "big", FrontierOrdinal: 8},
		{ID: "sibling-tool", Kind: "tool.result", IsActiveBranch: false, SummaryRef: "summary://tool/sibling", FrontierOrdinal: 9},
	})

	if out[1].Class != ClassSummaryOnly || !out[1].Include || out[1].SummaryRef != "summary://tool/new" || out[1].Text != "" {
		t.Fatalf("expected latest active tool summary_only, got %#v", out[1])
	}
	if out[2].Include {
		t.Fatalf("expected sibling tool dropped by default, got %#v", out[2])
	}
}

func TestSelectWithExplanationAssignsInclusionAndExclusionReasons(t *testing.T) {
	t.Parallel()

	selected, explanation := SelectWithExplanation([]Candidate{
		{ID: "u1", Kind: "turn.user", IsActiveBranch: true, IsCurrentUserTurn: true, Text: "ask", FrontierOrdinal: 10},
		{ID: "sum1", Kind: "summary.checkpoint", IsActiveBranch: true, IsNearestSummary: true, IsCheckpoint: true, SummaryRef: "summary://checkpoint/1", FrontierOrdinal: 9},
		{ID: "tool1", Kind: "tool.result", IsActiveBranch: true, IsLatestToolResult: true, SummaryRef: "summary://tool/1", FrontierOrdinal: 8},
		{ID: "sib1", Kind: "turn.agent", IsActiveBranch: false, FrontierOrdinal: 7},
		{ID: "dbg1", Kind: "packet.sent", IsActiveBranch: true, FrontierOrdinal: 6},
	})

	if len(selected) != 5 {
		t.Fatalf("unexpected selected size: %d", len(selected))
	}
	if len(explanation.Included) != 3 {
		t.Fatalf("expected 3 included items, got %#v", explanation.Included)
	}
	if explanation.Included[0].Reason != ReasonRecentTurn || explanation.Included[1].Reason != ReasonSummaryCheckpoint || explanation.Included[2].Reason != ReasonToolResult {
		t.Fatalf("unexpected inclusion reasons: %#v", explanation.Included)
	}
	if len(explanation.Excluded) != 2 {
		t.Fatalf("expected 2 excluded items, got %#v", explanation.Excluded)
	}
	if explanation.Excluded[0].Reason != ReasonNonLocal || explanation.Excluded[1].Reason != ReasonDebugNever {
		t.Fatalf("unexpected exclusion reasons: %#v", explanation.Excluded)
	}
}

func TestSelectWithExplanationOrdersDominantReasonsDeterministically(t *testing.T) {
	t.Parallel()

	_, explanation := SelectWithExplanation([]Candidate{
		{ID: "a", Kind: "turn.agent", IsActiveBranch: true, Text: "one", FrontierOrdinal: 1},
		{ID: "b", Kind: "turn.agent", IsActiveBranch: true, Text: "two", FrontierOrdinal: 2},
		{ID: "c", Kind: "turn.user", IsActiveBranch: true, IsCurrentUserTurn: true, Text: "latest", FrontierOrdinal: 3},
		{ID: "d", Kind: "turn.agent", IsActiveBranch: false, FrontierOrdinal: 4},
		{ID: "e", Kind: "tool.failure", IsActiveBranch: true, IsSensitiveLocal: true, FrontierOrdinal: 5},
		{ID: "f", Kind: "trace.debug", IsActiveBranch: true, FrontierOrdinal: 6},
	})

	if got := explanation.DominantInclusionReasons; len(got) < 2 || got[0].Reason != ReasonBranchLocality || got[0].Count != 2 || got[1].Reason != ReasonRecentTurn || got[1].Count != 1 {
		t.Fatalf("unexpected dominant inclusion reasons: %#v", got)
	}
	if got := explanation.DominantExclusionReasons; len(got) != 3 || got[0].Reason != ReasonDebugNever || got[1].Reason != ReasonNonLocal || got[2].Reason != ReasonSensitiveLocal {
		t.Fatalf("unexpected dominant exclusion reasons ordering: %#v", got)
	}
}

func TestSelectWithExplanationBoundsExcludedSamples(t *testing.T) {
	t.Parallel()

	candidates := make([]Candidate, 0, 10)
	for i := 0; i < 10; i++ {
		candidates = append(candidates, Candidate{
			ID:              "sibling-" + string(rune('a'+i)),
			Kind:            "turn.agent",
			IsActiveBranch:  false,
			FrontierOrdinal: i,
		})
	}

	_, explanation := SelectWithExplanation(candidates)
	if len(explanation.Excluded) != maxExcluded {
		t.Fatalf("expected %d excluded samples, got %d", maxExcluded, len(explanation.Excluded))
	}
	if explanation.DominantExclusionReasons[0].Reason != ReasonNonLocal || explanation.DominantExclusionReasons[0].Count != 10 {
		t.Fatalf("unexpected dominant exclusion counts: %#v", explanation.DominantExclusionReasons)
	}
}
