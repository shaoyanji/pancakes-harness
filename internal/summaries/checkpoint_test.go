package summaries

import "testing"

func TestSummaryCheckpointValidate(t *testing.T) {
	t.Parallel()

	cp := SummaryCheckpoint{
		SummaryID:    "sum-1",
		BranchID:     "main",
		BasisEventID: "e2",
		CoveredRange: CoveredRange{
			StartEventID: "e1",
			EndEventID:   "e2",
		},
		BlobRef:          "blob://sum-1",
		ByteEstimate:     128,
		TokenEstimate:    40,
		FreshnessVersion: 1,
	}
	if err := cp.Validate(); err != nil {
		t.Fatalf("expected valid checkpoint, got %v", err)
	}
}

func TestSummaryCheckpointValidateRejectsMissingRefs(t *testing.T) {
	t.Parallel()

	cp := SummaryCheckpoint{
		SummaryID:    "sum-1",
		BranchID:     "main",
		BasisEventID: "e2",
		CoveredRange: CoveredRange{
			StartEventID: "e1",
			EndEventID:   "e2",
		},
	}
	if err := cp.Validate(); err == nil {
		t.Fatal("expected missing text/blob ref to fail")
	}
}
