package egress

import (
	"strings"
	"testing"
)

func TestDebugMetricsNeverEnterEgress(t *testing.T) {
	t.Parallel()

	in := []Candidate{
		{ID: "e1", Kind: "packet.sent", FrontierOrdinal: 0},
		{ID: "e2", Kind: "metrics.snapshot", FrontierOrdinal: 1},
		{ID: "e3", Kind: "trace.debug", FrontierOrdinal: 2},
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
		{ID: "u1", Kind: "turn.user", Text: "hello", FrontierOrdinal: 0},
		{ID: "t1", Kind: "tool.result", Text: "huge payload", SummaryRef: "summary://tool/c1", FrontierOrdinal: 1, IsLatestToolResult: true},
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
		{ID: "u1", Kind: "turn.user", Text: "latest ask", FrontierOrdinal: 3, IsCurrentUserTurn: true},
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
		{ID: "old", Kind: "turn.agent", Text: large, BlobRef: "blob://old", FrontierOrdinal: 0},
		{ID: "new", Kind: "turn.user", Text: "latest", FrontierOrdinal: 20, IsCurrentUserTurn: true},
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
