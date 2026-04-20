// Package compactor implements Gemini-powered context compaction that converts
// a session's event spine into a structured MemoryLeaflet AST.
//
// The AST follows a Fibonacci heap-inspired tier structure:
//
//	depth 0: raw or lightly summarized events (leaves)
//	depth 1: thematic clusters (2–8 related events)
//	depth 2: major conversation sections
//	depth 3: root summary (whole conversation)
//
// Each level's node count follows approximate Fibonacci ratios:
// depth 3 has 1 root, depth 2 has 2–3 section nodes, depth 1 has
// 5–8 cluster nodes, depth 0 absorbs the remaining events.
//
// The compactor never mutates the event spine. It reads events,
// calls Gemini with a structured responseSchema, and writes the
// resulting AST as a checkpoint blob.
package compactor

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"pancakes-harness/internal/eventlog"
)

var (
	ErrEmptyLeaf         = errors.New("memory leaf has no content")
	ErrInvalidDepth      = errors.New("invalid leaf depth")
	ErrInvalidImportance = errors.New("importance must be 0.0–1.0")
	ErrASTTooDeep        = errors.New("AST exceeds max depth")
	ErrCircularRef       = errors.New("circular leaf reference")
	ErrSchemaValidation  = errors.New("schema validation failed")
	ErrEmptyRoot         = errors.New("AST must have exactly one root")
)

const (
	MaxASTDepth           = 4
	MaxLeavesPerCluster   = 8
	MinLeavesPerCluster   = 2
	MaxClustersPerSection = 5
	MaxSections           = 3
)

// LeafKind classifies what a memory leaf represents.
type LeafKind string

const (
	LeafKindEvent   LeafKind = "event"   // depth 0: single event
	LeafKindCluster LeafKind = "cluster" // depth 1: thematic group
	LeafKindSection LeafKind = "section" // depth 2: major section
	LeafKindRoot    LeafKind = "root"    // depth 3: conversation root
)

// MemoryLeaf is the fundamental node in the token AST.
// It forms a tree where each leaf has 0..N children.
// The tree is a DAG — no circular references allowed.
type MemoryLeaf struct {
	// Identity
	ID   string   `json:"id"`
	Kind LeafKind `json:"kind"`

	// Tree position
	Depth    int      `json:"depth"`
	ParentID string   `json:"parent_id,omitempty"`
	ChildIDs []string `json:"child_ids,omitempty"`

	// Content (at least one must be non-empty)
	Summary   string   `json:"summary,omitempty"`
	FullText  string   `json:"full_text,omitempty"`   // only for depth-0 leaves
	EventRefs []string `json:"event_refs,omitempty"`   // event IDs backing this leaf

	// Ranking
	Importance float64 `json:"importance"` // 0.0–1.0, higher = more important
	Timestamp  string  `json:"timestamp"`  // ISO 8601 of the covered time range midpoint

	// Metadata
	Tags     []string `json:"tags,omitempty"`
	ByteSize int      `json:"byte_size,omitempty"` // estimated bytes this leaf represents
}

// Validate checks structural integrity of a single leaf.
func (l MemoryLeaf) Validate() error {
	if l.ID == "" {
		return ErrEmptyLeaf
	}
	if l.Summary == "" && l.FullText == "" && len(l.EventRefs) == 0 {
		return ErrEmptyLeaf
	}
	if l.Depth < 0 || l.Depth >= MaxASTDepth {
		return ErrInvalidDepth
	}
	if l.Importance < 0.0 || l.Importance > 1.0 {
		return ErrInvalidImportance
	}
	switch l.Kind {
	case LeafKindEvent:
		if l.Depth != 0 {
			return ErrSchemaValidation
		}
	case LeafKindCluster:
		if l.Depth != 1 {
			return ErrSchemaValidation
		}
	case LeafKindSection:
		if l.Depth != 2 {
			return ErrSchemaValidation
		}
	case LeafKindRoot:
		if l.Depth != 3 {
			return ErrSchemaValidation
		}
	default:
		return ErrSchemaValidation
	}
	return nil
}

// TokenAST is the full compaction output — a tree of MemoryLeaf nodes
// produced by a single Gemini compaction pass.
type TokenAST struct {
	// Metadata
	SessionID    string `json:"session_id"`
	BranchID     string `json:"branch_id"`
	CompactedAt  string `json:"compacted_at"` // ISO 8601
	ModelUsed    string `json:"model_used"`   // e.g. "gemini-2.5-flash"
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`

	// The tree
	RootID string                `json:"root_id"`
	Leaves map[string]MemoryLeaf `json:"leaves"` // id → leaf

	// Coverage
	EventCoverage []string `json:"event_coverage"` // all event IDs covered by this AST
	ByteReduction int      `json:"byte_reduction"` // bytes saved vs raw events
}

// Validate checks that the AST is structurally sound.
func (ast TokenAST) Validate() error {
	if ast.RootID == "" {
		return ErrEmptyRoot
	}
	root, ok := ast.Leaves[ast.RootID]
	if !ok {
		return ErrEmptyRoot
	}
	if err := root.Validate(); err != nil {
		return fmt.Errorf("root leaf: %w", err)
	}
	if root.Kind != LeafKindRoot {
		return ErrEmptyRoot
	}

	visited := make(map[string]bool)
	return ast.validateSubtree(ast.RootID, visited)
}

func (ast TokenAST) validateSubtree(id string, visited map[string]bool) error {
	if visited[id] {
		return ErrCircularRef
	}
	visited[id] = true

	leaf, ok := ast.Leaves[id]
	if !ok {
		return fmt.Errorf("leaf %q referenced but not defined", id)
	}
	if err := leaf.Validate(); err != nil {
		return fmt.Errorf("leaf %q: %w", id, err)
	}

	if leaf.ParentID != "" {
		parent, ok := ast.Leaves[leaf.ParentID]
		if !ok {
			return fmt.Errorf("leaf %q parent %q not found", id, leaf.ParentID)
		}
		if parent.Depth != leaf.Depth+1 {
			return fmt.Errorf("leaf %q depth %d, parent %q depth %d (want %d)",
				id, leaf.Depth, leaf.ParentID, parent.Depth, leaf.Depth+1)
		}
	}

	for _, childID := range leaf.ChildIDs {
		if err := ast.validateSubtree(childID, visited); err != nil {
			return err
		}
	}
	return nil
}

// GetLeavesByDepth returns all leaves at the given depth, sorted by importance.
func (ast TokenAST) GetLeavesByDepth(depth int) []MemoryLeaf {
	var result []MemoryLeaf
	for _, leaf := range ast.Leaves {
		if leaf.Depth == depth {
			result = append(result, leaf)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Importance > result[j].Importance
	})
	return result
}

// TierStat describes how well a depth level matches the Fibonacci target.
type TierStat struct {
	Depth     int     `json:"depth"`
	Count     int     `json:"count"`
	Target    int     `json:"target"`
	Deviation float64 `json:"deviation"`
}

// GetFibonacciTierStats returns the count of leaves at each depth level.
func (ast TokenAST) GetFibonacciTierStats() []TierStat {
	counts := make(map[int]int)
	for _, leaf := range ast.Leaves {
		counts[leaf.Depth]++
	}
	targets := []int{13, 5, 2, 1} // depth 0 → 3
	stats := make([]TierStat, 0, MaxASTDepth)
	for d := 0; d < MaxASTDepth; d++ {
		target := 1
		if d < len(targets) {
			target = targets[d]
		}
		stats = append(stats, TierStat{
			Depth:     d,
			Count:     counts[d],
			Target:    target,
			Deviation: float64(counts[d]) - float64(target),
		})
	}
	return stats
}

// --- Gemini response schema types ---

// GeminiCompactResponse is the top-level structured output from Gemini.
type GeminiCompactResponse struct {
	Root    GeminiLeafNode `json:"root"`
	Summary string         `json:"summary"`
	Metrics GeminiMetrics  `json:"metrics"`
}

// GeminiLeafNode is the Gemini-internal representation before
// normalization into the canonical MemoryLeaf tree.
type GeminiLeafNode struct {
	ID         string           `json:"id"`
	Kind       string           `json:"kind"`
	Summary    string           `json:"summary"`
	EventRefs  []string         `json:"event_refs,omitempty"`
	Tags       []string         `json:"tags,omitempty"`
	Importance float64          `json:"importance"`
	Children   []GeminiLeafNode `json:"children,omitempty"`
}

// GeminiMetrics captures Gemini's assessment of the compaction.
type GeminiMetrics struct {
	ThemesIdentified int     `json:"themes_identified"`
	EventsProcessed  int     `json:"events_processed"`
	CompressionRatio float64 `json:"compression_ratio"`
	ConfidenceScore  float64 `json:"confidence_score"`
}

// ResponseSchema returns the JSON schema object for Gemini's responseSchema.
// This forces Gemini to produce valid MemoryLeaflet output at inference level.
func ResponseSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"root", "summary", "metrics"},
		"properties": map[string]any{
			"root":    leafNodeSchema(0),
			"summary": map[string]any{"type": "string", "description": "One-paragraph distillation of the conversation essence"},
			"metrics": map[string]any{
				"type":     "object",
				"required": []string{"themes_identified", "events_processed", "compression_ratio", "confidence_score"},
				"properties": map[string]any{
					"themes_identified": map[string]any{"type": "integer"},
					"events_processed":  map[string]any{"type": "integer"},
					"compression_ratio": map[string]any{"type": "number"},
					"confidence_score":  map[string]any{"type": "number"},
				},
			},
		},
	}
}

func leafNodeSchema(depth int) map[string]any {
	schema := map[string]any{
		"type":     "object",
		"required": []string{"id", "kind", "summary", "importance"},
		"properties": map[string]any{
			"id":         map[string]any{"type": "string", "description": "Unique leaf identifier"},
			"kind":       map[string]any{"type": "string", "enum": []string{"event", "cluster", "section", "root"}},
			"summary":    map[string]any{"type": "string", "description": "Essence, not word reduction"},
			"event_refs": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"tags":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"importance": map[string]any{"type": "number", "description": "0.0–1.0 within this tier"},
		},
	}
	if depth < MaxASTDepth-1 {
		schema["properties"].(map[string]any)["children"] = map[string]any{
			"type":  "array",
			"items": leafNodeSchema(depth + 1),
		}
	}
	return schema
}

// ParseGeminiResponse parses Gemini's JSON output into the canonical AST.
func ParseGeminiResponse(raw []byte, sessionID, branchID string) (TokenAST, error) {
	var resp GeminiCompactResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return TokenAST{}, fmt.Errorf("parse gemini response: %w", err)
	}

	ast := TokenAST{
		SessionID:   sessionID,
		BranchID:    branchID,
		CompactedAt: time.Now().UTC().Format(time.RFC3339),
		ModelUsed:   "gemini-2.5-flash",
		Leaves:      make(map[string]MemoryLeaf),
	}

	if err := ast.flattenGeminiNode(resp.Root, "", 0); err != nil {
		return TokenAST{}, err
	}
	ast.RootID = resp.Root.ID

	for _, leaf := range ast.Leaves {
		ast.EventCoverage = append(ast.EventCoverage, leaf.EventRefs...)
	}
	ast.EventCoverage = deduplicateStrings(ast.EventCoverage)

	return ast, nil
}

func (ast *TokenAST) flattenGeminiNode(node GeminiLeafNode, parentID string, treeDepth int) error {
	kind, err := parseLeafKind(node.Kind)
	if err != nil {
		return err
	}

	var childIDs []string
	for _, child := range node.Children {
		childIDs = append(childIDs, child.ID)
		if err := ast.flattenGeminiNode(child, node.ID, treeDepth+1); err != nil {
			return err
		}
	}

	// Convert Gemini's tree-from-root depth (0=root) to Fibonacci tier depth (3=root, 0=events).
	// This matches the kind validation: root=3, section=2, cluster=1, event=0.
	fibDepth := MaxASTDepth - 1 - treeDepth
	if fibDepth < 0 {
		fibDepth = 0
	}

	leaf := MemoryLeaf{
		ID:         node.ID,
		Kind:       kind,
		Depth:      fibDepth,
		ParentID:   parentID,
		ChildIDs:   childIDs,
		Summary:    node.Summary,
		EventRefs:  node.EventRefs,
		Importance: node.Importance,
		Tags:       node.Tags,
	}

	ast.Leaves[leaf.ID] = leaf
	return nil
}

func parseLeafKind(s string) (LeafKind, error) {
	switch LeafKind(s) {
	case LeafKindEvent, LeafKindCluster, LeafKindSection, LeafKindRoot:
		return LeafKind(s), nil
	default:
		return "", fmt.Errorf("unknown leaf kind: %q", s)
	}
}

// BuildCompactionPrompt builds Gemini system + user prompt for compaction.
// Format instructions are at the end (recency effect).
func BuildCompactionPrompt(events []eventlog.SerializedEvent, branchID string) (systemPrompt, userPrompt string) {
	systemPrompt = strings.TrimSpace(`
You are a conversation compactor. Your job is to distill the essence of a conversation
history into a structured memory leaflet AST. This is NOT a summary that reduces words —
it is a distillation that extracts what matters.

Rules:
1. Group related events into thematic clusters (2–8 events each)
2. Group clusters into major sections (2–5 clusters each)
3. Create exactly one root node covering all sections
4. Every leaf must have an importance score (0.0–1.0) within its tier
5. Tags should be 1–3 word descriptors of the theme
6. Summaries must capture WHY something matters, not just WHAT happened
7. Event refs must use the original event IDs from the input
8. The tree depth must not exceed 4 levels

The output must be valid JSON matching the schema exactly.
`)

	eventLines := make([]string, 0, len(events))
	for _, ev := range events {
		eventLines = append(eventLines, formatEventForPrompt(ev))
	}
	eventBlock := strings.Join(eventLines, "\n---\n")

	userPrompt = fmt.Sprintf(`Branch: %s
Events to compact (%d total):

%s

---

OUTPUT FORMAT (MANDATORY):
Return JSON matching this structure exactly:
{
  "root": {
    "id": "root-0",
    "kind": "root",
    "summary": "...",
    "importance": 1.0,
    "children": [
      {
        "id": "section-0",
        "kind": "section",
        "summary": "...",
        "importance": 0.8,
        "children": [
          {
            "id": "cluster-0",
            "kind": "cluster",
            "summary": "...",
            "importance": 0.7,
            "event_refs": ["evt-1"],
            "tags": ["topic"],
            "children": [
              {
                "id": "leaf-0",
                "kind": "event",
                "summary": "...",
                "importance": 0.5,
                "event_refs": ["evt-1"]
              }
            ]
          }
        ]
      }
    ]
  },
  "summary": "One paragraph distillation",
  "metrics": {
    "themes_identified": N,
    "events_processed": N,
    "compression_ratio": 0.N,
    "confidence_score": 0.N
  }
}

Remember: Close with }`, branchID, len(events), eventBlock)

	return systemPrompt, userPrompt
}

func formatEventForPrompt(ev eventlog.SerializedEvent) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("[%s] %s (branch: %s)", ev.ID, ev.Kind, ev.BranchID))
	if ev.Text != "" {
		b.WriteString(fmt.Sprintf("\nContent: %s", truncate(ev.Text, 512)))
	}
	if ev.Summary != "" {
		b.WriteString(fmt.Sprintf("\nSummary: %s", ev.Summary))
	}
	return b.String()
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func deduplicateStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

// ApproximateCompressionRatio estimates AST compression vs raw events.
func ApproximateCompressionRatio(rawBytes int, ast TokenAST) float64 {
	if rawBytes == 0 {
		return 0
	}
	astBytes := estimateASTBytes(ast)
	if astBytes >= rawBytes {
		return 1.0
	}
	return math.Round((1.0-float64(astBytes)/float64(rawBytes))*100) / 100
}

func estimateASTBytes(ast TokenAST) int {
	total := 0
	for _, leaf := range ast.Leaves {
		total += len(leaf.Summary) + len(leaf.FullText)
		for _, tag := range leaf.Tags {
			total += len(tag)
		}
		total += len(leaf.ID) + 64
	}
	return total
}
