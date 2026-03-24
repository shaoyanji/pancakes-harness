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
	if out[2].Class != ClassPassthrough || !out[2].Include || out[2].Text != "latest question" {
		t.Fatalf("expected active head user passthrough, got %#v", out[2])
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
