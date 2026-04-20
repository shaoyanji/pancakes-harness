package compactor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"pancakes-harness/internal/eventlog"
	"pancakes-harness/internal/summaries"
)

// Compactor is the public interface for context compaction.
// Implementations may use Gemini, a mock, or future backends.
type Compactor interface {
	// CompactContext reads events from the spine and produces a TokenAST.
	CompactContext(ctx context.Context, req CompactRequest) (CompactResponse, error)

	// Name identifies the compactor backend.
	Name() string
}

// CompactRequest holds the inputs for a compaction pass.
type CompactRequest struct {
	SessionID    string
	BranchID     string
	Events       []eventlog.Event
	BudgetBytes  int // optional: if >0, compactor tries to stay under this
}

// CompactResponse holds the compaction output.
type CompactResponse struct {
	AST       TokenAST
	Checkpoint summaries.SummaryCheckpoint
	RawAST     []byte // serialized AST for blob storage
	Metrics    CompactMetrics
}

// CompactMetrics captures performance data about the compaction pass.
type CompactMetrics struct {
	InputEvents   int
	InputBytes    int
	OutputBytes   int
	CompressionPct float64
	InputTokens   int
	OutputTokens  int
	GeminiLatency time.Duration
	TotalLatency  time.Duration
}

// GeminiCompactor orchestrates the full compaction pipeline:
// events → serialize → Gemini call → parse AST → validate → checkpoint.
type GeminiCompactor struct {
	adapter *GeminiAdapter
}

// NewGeminiCompactor creates a compactor backed by Gemini structured output.
func NewGeminiCompactor(cfg GeminiConfig) *GeminiCompactor {
	return &GeminiCompactor{
		adapter: NewGeminiAdapter(cfg),
	}
}

// Name returns the compactor backend identifier.
func (c *GeminiCompactor) Name() string { return "gemini-flash" }

// CompactContext runs the full compaction pipeline.
func (c *GeminiCompactor) CompactContext(ctx context.Context, req CompactRequest) (CompactResponse, error) {
	totalStart := time.Now()

	if len(req.Events) == 0 {
		return CompactResponse{}, ErrEmptyLeaf
	}

	// Step 1: Serialize events for Gemini consumption
	serialized := make([]eventlog.SerializedEvent, 0, len(req.Events))
	inputBytes := 0
	for _, e := range req.Events {
		se := eventlog.SerializeForCompaction(e)
		serialized = append(serialized, se)
		inputBytes += len(se.Text) + len(se.Summary)
	}

	// Step 2: Call Gemini with structured output
	geminiResult, err := c.adapter.Compact(ctx, serialized, req.SessionID, req.BranchID)
	if err != nil {
		return CompactResponse{}, fmt.Errorf("gemini compact: %w", err)
	}

	// Step 3: Parse Gemini response into canonical AST
	ast, err := ParseGeminiResponse(geminiResult.RawJSON, req.SessionID, req.BranchID)
	if err != nil {
		return CompactResponse{}, fmt.Errorf("parse AST: %w", err)
	}

	// Step 4: Validate AST structure
	if err := ast.Validate(); err != nil {
		return CompactResponse{}, fmt.Errorf("validate AST: %w", err)
	}

	// Step 5: Enrich AST metadata
	ast.InputTokens = geminiResult.InputTokens
	ast.OutputTokens = geminiResult.OutputTokens

	// Step 6: Serialize AST for blob storage
	rawAST, err := json.Marshal(ast)
	if err != nil {
		return CompactResponse{}, fmt.Errorf("marshal AST: %w", err)
	}
	ast.ByteReduction = inputBytes - len(rawAST)

	// Step 7: Build summary checkpoint referencing this AST
	summaryID := fmt.Sprintf("gemini-compact-%s-%d", req.BranchID, time.Now().UnixNano())
	blobRef := fmt.Sprintf("blob://compact/%s/%s", req.SessionID, summaryID)

	var startEventID, endEventID string
	if len(req.Events) > 0 {
		startEventID = req.Events[0].ID
		endEventID = req.Events[len(req.Events)-1].ID
	}

	checkpoint := summaries.SummaryCheckpoint{
		SummaryID:    summaryID,
		BranchID:     req.BranchID,
		BasisEventID: endEventID,
		CoveredRange: summaries.CoveredRange{
			StartEventID: startEventID,
			EndEventID:   endEventID,
		},
		BlobRef:          blobRef,
		ByteEstimate:     len(rawAST),
		TokenEstimate:    ast.OutputTokens,
		FreshnessVersion: 1,
	}

	totalLatency := time.Since(totalStart)

	// Compute compression ratio
	compressionPct := 0.0
	if inputBytes > 0 && len(rawAST) < inputBytes {
		compressionPct = (1.0 - float64(len(rawAST))/float64(inputBytes)) * 100
	}

	return CompactResponse{
		AST:        ast,
		Checkpoint: checkpoint,
		RawAST:     rawAST,
		Metrics: CompactMetrics{
			InputEvents:    len(req.Events),
			InputBytes:     inputBytes,
			OutputBytes:    len(rawAST),
			CompressionPct: compressionPct,
			InputTokens:    geminiResult.InputTokens,
			OutputTokens:   geminiResult.OutputTokens,
			GeminiLatency:  geminiResult.Latency,
			TotalLatency:   totalLatency,
		},
	}, nil
}

// --- Mock compactor for testing ---

// MockCompactor implements Compactor for tests. Returns a fixed AST.
type MockCompactor struct {
	FixedAST *TokenAST
}

func (m *MockCompactor) Name() string { return "mock" }

func (m *MockCompactor) CompactContext(ctx context.Context, req CompactRequest) (CompactResponse, error) {
	if len(req.Events) == 0 {
		return CompactResponse{}, ErrEmptyLeaf
	}
	if m.FixedAST != nil {
		rawAST, _ := json.Marshal(m.FixedAST)
		return CompactResponse{
			AST:    *m.FixedAST,
			RawAST: rawAST,
			Metrics: CompactMetrics{
				InputEvents: len(req.Events),
			},
		}, nil
	}

	// Default: build a minimal AST from the events
	leaves := make(map[string]MemoryLeaf)
	var childIDs []string

	for i, e := range req.Events {
		leafID := fmt.Sprintf("leaf-%d", i)
		text := ""
		if e.Meta != nil {
			if t, ok := e.Meta["text"].(string); ok {
				text = t
			}
		}
		leaves[leafID] = MemoryLeaf{
			ID:         leafID,
			Kind:       LeafKindEvent,
			Depth:      0,
			ParentID:   "cluster-0",
			Summary:    truncate(text, 128),
			EventRefs:  []string{e.ID},
			Importance: 0.5,
			Timestamp:  e.TS.Format(time.RFC3339),
		}
		childIDs = append(childIDs, leafID)
	}

	leaves["cluster-0"] = MemoryLeaf{
		ID:         "cluster-0",
		Kind:       LeafKindCluster,
		Depth:      1,
		ParentID:   "section-0",
		ChildIDs:   childIDs,
		Summary:    fmt.Sprintf("%d events grouped", len(req.Events)),
		Importance: 0.7,
	}
	leaves["section-0"] = MemoryLeaf{
		ID:         "section-0",
		Kind:       LeafKindSection,
		Depth:      2,
		ParentID:   "root-0",
		ChildIDs:   []string{"cluster-0"},
		Summary:    "Main conversation section",
		Importance: 0.8,
	}
	leaves["root-0"] = MemoryLeaf{
		ID:         "root-0",
		Kind:       LeafKindRoot,
		Depth:      3,
		ChildIDs:   []string{"section-0"},
		Summary:    "Conversation root",
		Importance: 1.0,
	}

	ast := TokenAST{
		SessionID:   req.SessionID,
		BranchID:    req.BranchID,
		CompactedAt: time.Now().UTC().Format(time.RFC3339),
		ModelUsed:   "mock",
		RootID:      "root-0",
		Leaves:      leaves,
	}
	for _, e := range req.Events {
		ast.EventCoverage = append(ast.EventCoverage, e.ID)
	}

	rawAST, _ := json.Marshal(ast)
	return CompactResponse{
		AST:    ast,
		RawAST: rawAST,
		Metrics: CompactMetrics{
			InputEvents: len(req.Events),
			OutputBytes: len(rawAST),
		},
	}, nil
}
