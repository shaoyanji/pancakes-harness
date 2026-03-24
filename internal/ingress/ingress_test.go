package ingress

import (
	"context"
	"testing"
	"time"
)

func TestFingerprintSameLogicalRequestIsStable(t *testing.T) {
	t.Parallel()

	req := Request{
		SessionID:  "s1",
		BranchID:   "main",
		Task:       "summarize latest notes",
		Refs:       []string{"ref-a", "ref-b"},
		AllowTools: true,
		Constraints: map[string]string{
			"priority": "high",
			"mode":     "safe",
		},
	}

	fp1, err := FingerprintRequest(req)
	if err != nil {
		t.Fatalf("fingerprint 1: %v", err)
	}
	fp2, err := FingerprintRequest(req)
	if err != nil {
		t.Fatalf("fingerprint 2: %v", err)
	}
	if fp1 != fp2 {
		t.Fatalf("expected equal fingerprints, got %q vs %q", fp1, fp2)
	}
}

func TestFingerprintRefsReorderedIsStable(t *testing.T) {
	t.Parallel()

	inA := FingerprintInput{
		SessionID:  "s1",
		BranchID:   "main",
		Task:       "plan",
		Refs:       []string{"ref-c", "ref-a", "ref-b"},
		AllowTools: false,
	}
	inB := FingerprintInput{
		SessionID:  "s1",
		BranchID:   "main",
		Task:       "plan",
		Refs:       []string{"ref-b", "ref-c", "ref-a"},
		AllowTools: false,
	}

	fpA, err := Fingerprint(inA)
	if err != nil {
		t.Fatalf("fingerprint A: %v", err)
	}
	fpB, err := Fingerprint(inB)
	if err != nil {
		t.Fatalf("fingerprint B: %v", err)
	}
	if fpA != fpB {
		t.Fatalf("expected equal fingerprints, got %q vs %q", fpA, fpB)
	}
}

func TestFingerprintConstraintsReorderedIsStable(t *testing.T) {
	t.Parallel()

	c1 := map[string]string{}
	c1["model"] = "xs"
	c1["latency"] = "low"
	c1["policy"] = "strict"

	c2 := map[string]string{}
	c2["policy"] = "strict"
	c2["model"] = "xs"
	c2["latency"] = "low"

	inA := FingerprintInput{SessionID: "s1", BranchID: "main", Task: "route", Constraints: c1}
	inB := FingerprintInput{SessionID: "s1", BranchID: "main", Task: "route", Constraints: c2}

	fpA, err := Fingerprint(inA)
	if err != nil {
		t.Fatalf("fingerprint A: %v", err)
	}
	fpB, err := Fingerprint(inB)
	if err != nil {
		t.Fatalf("fingerprint B: %v", err)
	}
	if fpA != fpB {
		t.Fatalf("expected equal fingerprints, got %q vs %q", fpA, fpB)
	}
}

func TestFingerprintTaskChangeChangesFingerprint(t *testing.T) {
	t.Parallel()

	base := FingerprintInput{
		SessionID: "s1",
		BranchID:  "main",
		Task:      "task-one",
		Refs:      []string{"r1", "r2"},
		Constraints: map[string]string{
			"mode": "safe",
		},
		AllowTools: true,
	}
	changed := base
	changed.Task = "task-two"

	fpA, err := Fingerprint(base)
	if err != nil {
		t.Fatalf("fingerprint base: %v", err)
	}
	fpB, err := Fingerprint(changed)
	if err != nil {
		t.Fatalf("fingerprint changed: %v", err)
	}
	if fpA == fpB {
		t.Fatalf("expected different fingerprints for different tasks, both were %q", fpA)
	}
}

func TestInflightDedupeOneLeaderFollowersWait(t *testing.T) {
	t.Parallel()

	inf := NewInflight()
	leader := inf.Enter("fp-1")
	if !leader.Leader() {
		t.Fatal("expected first caller to be leader")
	}

	follower1 := inf.Enter("fp-1")
	if follower1.Leader() {
		t.Fatal("expected second caller to be follower")
	}
	follower2 := inf.Enter("fp-1")
	if follower2.Leader() {
		t.Fatal("expected third caller to be follower")
	}

	errCh := make(chan error, 2)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		errCh <- follower1.Wait(ctx)
	}()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		errCh <- follower2.Wait(ctx)
	}()

	select {
	case err := <-errCh:
		t.Fatalf("follower returned before leader done: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	leader.Done()

	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("follower wait failed: %v", err)
		}
	}

	next := inf.Enter("fp-1")
	if !next.Leader() {
		t.Fatal("expected new leader after prior request completion")
	}
	next.Done()
}
