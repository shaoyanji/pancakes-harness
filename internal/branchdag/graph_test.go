package branchdag

import "testing"

func TestBranchForkDoesNotCopyTranscript(t *testing.T) {
	t.Parallel()

	g := NewGraph()
	_, err := g.CreateBranch(Branch{BranchID: "main"})
	if err != nil {
		t.Fatalf("create main: %v", err)
	}
	_, err = g.AppendEvent("main", "e1")
	if err != nil {
		t.Fatalf("append e1: %v", err)
	}
	mainBeforeFork, err := g.AppendEvent("main", "e2")
	if err != nil {
		t.Fatalf("append e2: %v", err)
	}

	child, err := g.ForkBranch("alt", "main", "e2")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}

	if child.ParentBranchID != "main" {
		t.Fatalf("expected parent main, got %q", child.ParentBranchID)
	}
	if child.ForkEventID != "e2" {
		t.Fatalf("expected fork event e2, got %q", child.ForkEventID)
	}
	if child.HeadEventID != mainBeforeFork.HeadEventID {
		t.Fatalf("fork should point at same head, child=%q parent=%q", child.HeadEventID, mainBeforeFork.HeadEventID)
	}

	_, err = g.AppendEvent("alt", "a1")
	if err != nil {
		t.Fatalf("append child event: %v", err)
	}
	mainAfterChildAppend, err := g.GetBranch("main")
	if err != nil {
		t.Fatalf("get main: %v", err)
	}
	if mainAfterChildAppend.HeadEventID != "e2" {
		t.Fatalf("child append should not mutate parent head, got %q", mainAfterChildAppend.HeadEventID)
	}
}

func TestDirtyRangesUpdateAfterForkAndAppend(t *testing.T) {
	t.Parallel()

	g := NewGraph()
	_, err := g.CreateBranch(Branch{BranchID: "main"})
	if err != nil {
		t.Fatalf("create main: %v", err)
	}
	_, _ = g.AppendEvent("main", "e1")
	main, _ := g.AppendEvent("main", "e2")

	if len(main.DirtyRanges) != 1 {
		t.Fatalf("expected one dirty range, got %d", len(main.DirtyRanges))
	}
	if main.DirtyRanges[0].StartEventID != "e1" || main.DirtyRanges[0].EndEventID != "e2" {
		t.Fatalf("unexpected main dirty range: %#v", main.DirtyRanges[0])
	}

	alt, err := g.ForkBranch("alt", "main", "e2")
	if err != nil {
		t.Fatalf("fork alt: %v", err)
	}
	if len(alt.DirtyRanges) != 1 || alt.DirtyRanges[0].EndEventID != "e2" {
		t.Fatalf("expected inherited dirty range to e2, got %#v", alt.DirtyRanges)
	}

	alt, err = g.AppendEvent("alt", "a1")
	if err != nil {
		t.Fatalf("append alt: %v", err)
	}
	if alt.DirtyRanges[0].EndEventID != "a1" {
		t.Fatalf("expected alt dirty range end a1, got %#v", alt.DirtyRanges[0])
	}

	main, err = g.AppendEvent("main", "e3")
	if err != nil {
		t.Fatalf("append main: %v", err)
	}
	if main.DirtyRanges[0].EndEventID != "e3" {
		t.Fatalf("expected main dirty range end e3, got %#v", main.DirtyRanges[0])
	}
	if alt.DirtyRanges[0].EndEventID != "a1" {
		t.Fatalf("main append should not mutate alt dirty range, got %#v", alt.DirtyRanges[0])
	}
}
