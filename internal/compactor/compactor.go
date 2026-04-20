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
	SessionID   string
	BranchID    string
	Events      []eventlog.Event
	BudgetBytes int // optional: if >0, compactor tries to stay under this
}

// CompactResponse holds the compaction output.
type CompactResponse struct {
	AST        TokenAST
	Checkpoint summaries.SummaryCheckpoint
	RawAST     []byte // serialized AST for blob storage
	Metrics    CompactMetrics
}

// CompactMetrics captures performance data about the compaction pass.
type CompactMetrics struct {
	InputEvents    int
	InputBytes     int
	OutputBytes    int
	CompressionPct float64
	InputTokens    int
	OutputTokens   int
	GeminiLatency  time.Duration
	TotalLatency   time.Duration
	ChunkCount     int // how many chunks the input was split into
	MergePass      bool
}

const (
	// ChunkThresholdBytes is the input size above which we chunk-then-merge.
	// Research shows Gemini quality degrades for summarization above ~200k tokens.
	// At ~4 chars/token that's ~800KB, but we conservatively threshold on raw bytes.
	ChunkThresholdBytes = 400_000

	// MaxChunkEvents caps how many events go into a single Gemini call.
	MaxChunkEvents = 200
)

// GeminiCompactor orchestrates the full compaction pipeline:
// events → (chunk if needed) → Gemini calls → parse ASTs → merge → validate → checkpoint.
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

// CompactContext runs the full compaction pipeline with chunk-then-merge.
func (c *GeminiCompactor) CompactContext(ctx context.Context, req CompactRequest) (CompactResponse, error) {
	totalStart := time.Now()

	if len(req.Events) == 0 {
		return CompactResponse{}, ErrEmptyLeaf
	}

	// Step 1: Serialize all events and measure input size
	serialized := make([]eventlog.SerializedEvent, 0, len(req.Events))
	inputBytes := 0
	for _, e := range req.Events {
		se := eventlog.SerializeForCompaction(e)
		serialized = append(serialized, se)
		inputBytes += len(se.Text) + len(se.Summary)
	}

	// Step 2: Decide single-pass vs chunk-then-merge
	var ast TokenAST
	var totalInputTokens, totalOutputTokens int
	var totalGeminiLatency time.Duration
	chunkCount := 1
	mergePass := false

	if inputBytes <= ChunkThresholdBytes && len(req.Events) <= MaxChunkEvents {
		// Single-pass: input is small enough
		result, err := c.compactSinglePass(ctx, serialized, req.SessionID, req.BranchID)
		if err != nil {
			return CompactResponse{}, err
		}
		ast = result.ast
		totalInputTokens = result.inputTokens
		totalOutputTokens = result.outputTokens
		totalGeminiLatency = result.latency
	} else {
		// Chunk-then-merge
		chunks := splitIntoChunks(serialized, MaxChunkEvents)
		chunkCount = len(chunks)

		if chunkCount == 1 {
			// Shouldn't happen but handle gracefully
			result, err := c.compactSinglePass(ctx, chunks[0], req.SessionID, req.BranchID)
			if err != nil {
				return CompactResponse{}, err
			}
			ast = result.ast
			totalInputTokens = result.inputTokens
			totalOutputTokens = result.outputTokens
			totalGeminiLatency = result.latency
		} else {
			// Multi-pass: compact each chunk, then merge
			mergeResult, err := c.compactChunkAndMerge(ctx, chunks, req.SessionID, req.BranchID)
			if err != nil {
				return CompactResponse{}, err
			}
			ast = mergeResult.ast
			totalInputTokens = mergeResult.totalInputTokens
			totalOutputTokens = mergeResult.totalOutputTokens
			totalGeminiLatency = mergeResult.totalLatency
			mergePass = true
		}
	}

	// Step 3: Validate AST structure
	if err := ast.Validate(); err != nil {
		return CompactResponse{}, fmt.Errorf("validate AST: %w", err)
	}

	// Step 4: Enrich AST metadata
	ast.InputTokens = totalInputTokens
	ast.OutputTokens = totalOutputTokens

	// Step 5: Serialize AST for blob storage
	rawAST, err := json.Marshal(ast)
	if err != nil {
		return CompactResponse{}, fmt.Errorf("marshal AST: %w", err)
	}
	ast.ByteReduction = inputBytes - len(rawAST)

	// Step 6: Build summary checkpoint referencing this AST
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
			InputTokens:    totalInputTokens,
			OutputTokens:   totalOutputTokens,
			GeminiLatency:  totalGeminiLatency,
			TotalLatency:   totalLatency,
			ChunkCount:     chunkCount,
			MergePass:      mergePass,
		},
	}, nil
}

// singlePassResult holds a single compaction pass result.
type singlePassResult struct {
	ast          TokenAST
	inputTokens  int
	outputTokens int
	latency      time.Duration
}

// compactSinglePass sends one chunk of events to Gemini and returns the parsed AST.
func (c *GeminiCompactor) compactSinglePass(
	ctx context.Context,
	events []eventlog.SerializedEvent,
	sessionID, branchID string,
) (singlePassResult, error) {
	geminiResult, err := c.adapter.Compact(ctx, events, sessionID, branchID)
	if err != nil {
		return singlePassResult{}, fmt.Errorf("gemini compact: %w", err)
	}

	ast, err := ParseGeminiResponse(geminiResult.RawJSON, sessionID, branchID)
	if err != nil {
		return singlePassResult{}, fmt.Errorf("parse AST: %w", err)
	}

	return singlePassResult{
		ast:          ast,
		inputTokens:  geminiResult.InputTokens,
		outputTokens: geminiResult.OutputTokens,
		latency:      geminiResult.Latency,
	}, nil
}

// mergeResult holds the chunk-and-merge pipeline result.
type mergeResult struct {
	ast              TokenAST
	totalInputTokens int
	totalOutputTokens int
	totalLatency     time.Duration
}

// compactChunkAndMerge splits events into chunks, compacts each, then merges.
func (c *GeminiCompactor) compactChunkAndMerge(
	ctx context.Context,
	chunks [][]eventlog.SerializedEvent,
	sessionID, branchID string,
) (mergeResult, error) {
	var totalInputTokens, totalOutputTokens int
	var totalLatency time.Duration

	// Phase 1: Compact each chunk individually
	chunkASTs := make([]TokenAST, 0, len(chunks))
	chunkSummaries := make([]chunkSummary, 0, len(chunks))

	for i, chunk := range chunks {
		result, err := c.compactSinglePass(ctx, chunk, sessionID, branchID)
		if err != nil {
			return mergeResult{}, fmt.Errorf("chunk %d compaction: %w", i, err)
		}
		chunkASTs = append(chunkASTs, result.ast)
		totalInputTokens += result.inputTokens
		totalOutputTokens += result.outputTokens
		totalLatency += result.latency

		// Extract a compact summary from this chunk's AST for the merge pass
		root, ok := result.ast.Leaves[result.ast.RootID]
		if ok {
			cs := chunkSummary{
				ChunkIndex:    i,
				EventCount:    len(chunk),
				RootSummary:   root.Summary,
				Tags:          root.Tags,
				Importance:    root.Importance,
				EventCoverage: result.ast.EventCoverage,
			}
			// Also grab section-level summaries
			for _, leaf := range result.ast.GetLeavesByDepth(2) {
				cs.Sections = append(cs.Sections, sectionSummary{
					ID:         leaf.ID,
					Summary:    leaf.Summary,
					Importance: leaf.Importance,
					Tags:       leaf.Tags,
				})
			}
			chunkSummaries = append(chunkSummaries, cs)
		}
	}

	// Phase 2: Merge — feed chunk summaries back to Gemini for final synthesis
	mergeAST, err := c.mergeChunks(ctx, chunkASTs, chunkSummaries, sessionID, branchID)
	if err != nil {
		return mergeResult{}, fmt.Errorf("merge pass: %w", err)
	}

	return mergeResult{
		ast:               mergeAST,
		totalInputTokens:  totalInputTokens,
		totalOutputTokens: totalOutputTokens,
		totalLatency:      totalLatency,
	}, nil
}

// chunkSummary is a compact representation of one chunk's output for the merge pass.
type chunkSummary struct {
	ChunkIndex    int              `json:"chunk_index"`
	EventCount    int              `json:"event_count"`
	RootSummary   string           `json:"root_summary"`
	Tags          []string         `json:"tags,omitempty"`
	Importance    float64          `json:"importance"`
	EventCoverage []string         `json:"event_coverage"`
	Sections      []sectionSummary `json:"sections,omitempty"`
}

type sectionSummary struct {
	ID         string   `json:"id"`
	Summary    string   `json:"summary"`
	Importance float64  `json:"importance"`
	Tags       []string `json:"tags,omitempty"`
}

// mergeChunks sends chunk summaries to Gemini for final synthesis.
func (c *GeminiCompactor) mergeChunks(
	ctx context.Context,
	chunkASTs []TokenAST,
	summaries []chunkSummary,
	sessionID, branchID string,
) (TokenAST, error) {
	if len(summaries) == 0 {
		return TokenAST{}, fmt.Errorf("no chunk summaries to merge")
	}

	// If only one chunk, return its AST directly
	if len(summaries) == 1 {
		return chunkASTs[0], nil
	}

	// Build merge prompt
	systemPrompt, userPrompt := buildMergePrompt(summaries, branchID)

	// Call Gemini for the merge — reuse the same adapter but with merge-specific prompts
	mergeJSON, inputTokens, outputTokens, latency, err := c.adapter.CompactWithCustomPrompts(
		ctx, systemPrompt, userPrompt, sessionID, branchID,
	)
	if err != nil {
		return TokenAST{}, err
	}

	mergedAST, err := ParseGeminiResponse(mergeJSON, sessionID, branchID)
	if err != nil {
		return TokenAST{}, fmt.Errorf("parse merge AST: %w", err)
	}

	// Accumulate event coverage from all chunks
	allEventIDs := make(map[string]bool)
	for _, ast := range chunkASTs {
		for _, id := range ast.EventCoverage {
			allEventIDs[id] = true
		}
	}
	for id := range allEventIDs {
		mergedAST.EventCoverage = append(mergedAST.EventCoverage, id)
	}

	mergedAST.InputTokens = inputTokens
	mergedAST.OutputTokens = outputTokens

	// Accumulate latency across all chunk passes + merge
	for _, ast := range chunkASTs {
		_ = ast // latency tracked in caller
	}
	_ = latency // total latency tracked in caller

	return mergedAST, nil
}

// splitIntoChunks divides events into chronological chunks, each under maxPerChunk.
func splitIntoChunks(events []eventlog.SerializedEvent, maxPerChunk int) [][]eventlog.SerializedEvent {
	if len(events) == 0 {
		return nil
	}
	if len(events) <= maxPerChunk {
		return [][]eventlog.SerializedEvent{events}
	}

	var chunks [][]eventlog.SerializedEvent
	for i := 0; i < len(events); i += maxPerChunk {
		end := i + maxPerChunk
		if end > len(events) {
			end = len(events)
		}
		chunks = append(chunks, events[i:end])
	}
	return chunks
}

// buildMergePrompt creates the Gemini prompt for the merge pass.
// The merge pass takes chunk summaries and synthesizes a single root AST.
func buildMergePrompt(summaries []chunkSummary, branchID string) (systemPrompt, userPrompt string) {
	systemPrompt = `You are a conversation merge compactor. You receive summaries from multiple
compaction chunks of a single conversation. Your job is to synthesize them into
one unified MemoryLeaflet AST.

Rules:
1. Merge thematic clusters across chunks where topics overlap
2. Preserve chronological ordering
3. Deduplicate repeated themes
4. The root must cover ALL chunks — nothing can be lost
5. Summaries must capture essence, not just concatenate
6. Every leaf needs an importance score (0.0–1.0)
7. Tags should be 1–3 word descriptors
8. The output must be valid JSON matching the schema exactly.`

	// Serialize chunk summaries
	summaryLines := make([]string, 0, len(summaries))
	for _, cs := range summaries {
		line := fmt.Sprintf("Chunk %d (%d events): %s", cs.ChunkIndex, cs.EventCount, cs.RootSummary)
		if len(cs.Sections) > 0 {
			line += "\nSections:"
			for _, sec := range cs.Sections {
				line += fmt.Sprintf("\n  - %s (importance: %.1f): %s", sec.ID, sec.Importance, sec.Summary)
			}
		}
		if len(cs.Tags) > 0 {
			line += fmt.Sprintf("\nTags: %v", cs.Tags)
		}
		summaryLines = append(summaryLines, line)
	}
	summaryBlock := fmt.Sprintf("%s\n---\n", joinLines(summaryLines))

	userPrompt = fmt.Sprintf(`Branch: %s
Chunk summaries to merge (%d chunks):

%s

---

OUTPUT FORMAT (MANDATORY):
Return JSON matching this structure:
{
  "root": {
    "id": "root-0",
    "kind": "root",
    "summary": "...",
    "importance": 1.0,
    "children": [...]
  },
  "summary": "Unified distillation across all chunks",
  "metrics": {
    "themes_identified": N,
    "events_processed": N,
    "compression_ratio": 0.N,
    "confidence_score": 0.N
  }
}

Remember: Close with }`, branchID, len(summaries), summaryBlock)

	return systemPrompt, userPrompt
}

func joinLines(lines []string) string {
	result := ""
	for i, line := range lines {
		if i > 0 {
			result += "\n\n"
		}
		result += line
	}
	return result
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
