package assembler

import (
	"errors"
	"strings"
	"testing"
)

func TestAssembleEnforces14336ByteEnvelope(t *testing.T) {
	t.Parallel()

	req := Request{
		Method: "POST",
		Path:   "/v1/responses",
		Headers: []Header{
			{Name: "Authorization", Value: "Bearer test-token"},
			{Name: "Content-Type", Value: "application/json"},
		},
		Body: PacketBody{
			SessionID:    "s1",
			BranchHandle: "b:main",
			WorkingSet: []WorkingItem{
				{
					ID:              "w1",
					Kind:            "turn.user",
					Text:            strings.Repeat("x", 30000),
					FrontierOrdinal: 1,
				},
			},
		},
	}

	_, err := Assemble(req)
	if !errors.Is(err, ErrPacketRejectedBudget) {
		t.Fatalf("expected ErrPacketRejectedBudget, got %v", err)
	}
}

func TestLargeBlobTextNeverShippedByDefault(t *testing.T) {
	t.Parallel()

	large := strings.Repeat("BLOB-CONTENT-", 200)
	req := Request{
		Method: "POST",
		Path:   "/v1/responses",
		Headers: []Header{
			{Name: "Content-Type", Value: "application/json"},
		},
		Body: PacketBody{
			SessionID:    "s2",
			BranchHandle: "b:main",
			WorkingSet: []WorkingItem{
				{
					ID:              "blob-item",
					Kind:            "tool.result",
					Text:            large,
					BlobRef:         "blob://tool-output",
					FrontierOrdinal: 1,
				},
			},
		},
	}

	result, err := Assemble(req)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if BodyContainsText(result.BodyJSON, large) {
		t.Fatal("large text should not be shipped when blob_ref is present")
	}
	if !BodyContainsText(result.BodyJSON, "blob://tool-output") {
		t.Fatal("blob ref must be kept in payload")
	}
}

func TestCompactionIsDeterministic(t *testing.T) {
	t.Parallel()

	req := Request{
		Method: "POST",
		Path:   "/v1/responses",
		Headers: []Header{
			{Name: "Z-Header", Value: "z"},
			{Name: "A-Header", Value: "a"},
		},
		Body: PacketBody{
			SessionID:            "s3",
			BranchHandle:         "b:alt",
			CheckpointSummaryRef: "summary://checkpoint",
			Debug:                []string{"trace", "verbose"},
			Provenance:           []string{"p1", "p2"},
			WorkingSet: []WorkingItem{
				{
					ID:                 "w2",
					Kind:               "turn.agent",
					Text:               strings.Repeat("y", 9000),
					SummaryRef:         "summary://w2",
					BlobRef:            "blob://w2",
					FrontierOrdinal:    2,
					Provenance:         "optional",
					ProvenanceRequired: false,
				},
				{
					ID:                 "w1",
					Kind:               "turn.user",
					Text:               strings.Repeat("z", 9000),
					SummaryRef:         "summary://w1",
					BlobRef:            "blob://w1",
					FrontierOrdinal:    1,
					Provenance:         "required",
					ProvenanceRequired: true,
				},
			},
		},
	}

	first, err := Assemble(req)
	if err != nil {
		t.Fatalf("first assemble: %v", err)
	}
	second, err := Assemble(req)
	if err != nil {
		t.Fatalf("second assemble: %v", err)
	}

	if first.Stage != second.Stage {
		t.Fatalf("stage mismatch: %d vs %d", first.Stage, second.Stage)
	}
	if string(first.BodyJSON) != string(second.BodyJSON) {
		t.Fatalf("body mismatch:\nfirst=%s\nsecond=%s", string(first.BodyJSON), string(second.BodyJSON))
	}
	if first.Measurement != second.Measurement {
		t.Fatalf("measurement mismatch: %#v vs %#v", first.Measurement, second.Measurement)
	}
	if first.Measurement.EnvelopeBytes > MaxEnvelopeBytes {
		t.Fatalf("envelope exceeds max: %d", first.Measurement.EnvelopeBytes)
	}
}

func TestFollowupPacketIncludesMinimalToolExcerptOnly(t *testing.T) {
	t.Parallel()

	fullPayload := strings.Repeat("TOOL-PAYLOAD-", 400)
	req := Request{
		Method: "POST",
		Path:   "/v1/responses",
		Headers: []Header{
			{Name: "Content-Type", Value: "application/json"},
		},
		Body: PacketBody{
			SessionID:    "s4",
			BranchHandle: "b:main",
			WorkingSet: []WorkingItem{
				{
					ID:              "tool-1",
					Kind:            "tool.result",
					Text:            fullPayload,
					SummaryRef:      "summary://tool/c1",
					BlobRef:         "blob://tool/full",
					FrontierOrdinal: 3,
				},
			},
		},
	}

	result, err := Assemble(req)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}

	if BodyContainsText(result.BodyJSON, fullPayload) {
		t.Fatal("follow-up packet should not include full tool payload")
	}
	if !BodyContainsText(result.BodyJSON, "summary://tool/c1") {
		t.Fatal("follow-up packet should keep tool summary ref")
	}
	if !BodyContainsText(result.BodyJSON, "blob://tool/full") {
		t.Fatal("follow-up packet should keep blob ref for local replay/reuse")
	}
}

func TestAssembleExternalContextIncludedWhenProvided(t *testing.T) {
	t.Parallel()

	external := "AURA:FIXED:context-block:v1"
	req := Request{
		Method: "POST",
		Path:   "/v1/responses",
		Headers: []Header{
			{Name: "Content-Type", Value: "application/json"},
		},
		Body: PacketBody{
			SessionID:       "s-ext",
			BranchHandle:    "b:main",
			ExternalContext: external,
			WorkingSet: []WorkingItem{
				{
					ID:              "w1",
					Kind:            "turn.user",
					Text:            "hello",
					FrontierOrdinal: 1,
				},
			},
		},
	}

	result, err := Assemble(req)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if result.ExternalContextStatus != "included" {
		t.Fatalf("expected included status, got %q", result.ExternalContextStatus)
	}
	if result.ExternalContextBytes != len(external) {
		t.Fatalf("expected external bytes %d, got %d", len(external), result.ExternalContextBytes)
	}
	if !BodyContainsText(result.BodyJSON, external) {
		t.Fatalf("expected external context to be present in body json: %s", string(result.BodyJSON))
	}
}

func TestAssembleExternalContextDroppedBeforeCompaction(t *testing.T) {
	t.Parallel()

	req := Request{
		Method: "POST",
		Path:   "/v1/responses",
		Headers: []Header{
			{Name: "Content-Type", Value: "application/json"},
		},
		Body: PacketBody{
			SessionID:       "s-ext-drop",
			BranchHandle:    "b:main",
			ExternalContext: strings.Repeat("E", 14000),
			WorkingSet: []WorkingItem{
				{
					ID:              "w1",
					Kind:            "turn.user",
					Text:            "small body remains valid",
					FrontierOrdinal: 1,
				},
			},
		},
	}

	result, err := Assemble(req)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if result.Stage != 0 {
		t.Fatalf("expected stage 0 after dropping external context, got %d", result.Stage)
	}
	if result.ExternalContextStatus != "dropped_budget" {
		t.Fatalf("expected dropped_budget status, got %q", result.ExternalContextStatus)
	}
	if result.ExternalContextBytes != 14000 {
		t.Fatalf("expected external_context_bytes=14000, got %d", result.ExternalContextBytes)
	}
	if BodyContainsText(result.BodyJSON, strings.Repeat("E", 32)) {
		t.Fatalf("external context should be omitted from body json after budget drop: %s", string(result.BodyJSON))
	}
}
