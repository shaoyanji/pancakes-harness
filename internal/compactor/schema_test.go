package compactor

import (
	"encoding/json"
	"testing"
)

func TestMemoryLeafValidate(t *testing.T) {
	tests := []struct {
		name    string
		leaf    MemoryLeaf
		wantErr error
	}{
		{
			name: "valid depth-0 event leaf",
			leaf: MemoryLeaf{
				ID:         "leaf-0",
				Kind:       LeafKindEvent,
				Depth:      0,
				Summary:    "user asked about X",
				EventRefs:  []string{"evt-1"},
				Importance: 0.5,
			},
			wantErr: nil,
		},
		{
			name: "valid depth-1 cluster leaf",
			leaf: MemoryLeaf{
				ID:         "cluster-0",
				Kind:       LeafKindCluster,
				Depth:      1,
				ParentID:   "section-0",
				ChildIDs:   []string{"leaf-0", "leaf-1"},
				Summary:    "topic discussion",
				Importance: 0.7,
			},
			wantErr: nil,
		},
		{
			name: "valid depth-3 root leaf",
			leaf: MemoryLeaf{
				ID:         "root-0",
				Kind:       LeafKindRoot,
				Depth:      3,
				ChildIDs:   []string{"section-0"},
				Summary:    "conversation overview",
				Importance: 1.0,
			},
			wantErr: nil,
		},
		{
			name: "empty ID fails",
			leaf: MemoryLeaf{
				Kind:       LeafKindEvent,
				Depth:      0,
				Summary:    "test",
				Importance: 0.5,
			},
			wantErr: ErrEmptyLeaf,
		},
		{
			name: "no content fails",
			leaf: MemoryLeaf{
				ID:         "leaf-0",
				Kind:       LeafKindEvent,
				Depth:      0,
				Importance: 0.5,
			},
			wantErr: ErrEmptyLeaf,
		},
		{
			name: "invalid depth fails",
			leaf: MemoryLeaf{
				ID:         "leaf-0",
				Kind:       LeafKindEvent,
				Depth:      10,
				Summary:    "test",
				Importance: 0.5,
			},
			wantErr: ErrInvalidDepth,
		},
		{
			name: "importance > 1.0 fails",
			leaf: MemoryLeaf{
				ID:         "leaf-0",
				Kind:       LeafKindEvent,
				Depth:      0,
				Summary:    "test",
				Importance: 1.5,
			},
			wantErr: ErrInvalidImportance,
		},
		{
			name: "kind/depth mismatch fails",
			leaf: MemoryLeaf{
				ID:         "leaf-0",
				Kind:       LeafKindRoot,
				Depth:      0,
				Summary:    "test",
				Importance: 0.5,
			},
			wantErr: ErrSchemaValidation,
		},
		{
			name: "negative importance fails",
			leaf: MemoryLeaf{
				ID:         "leaf-0",
				Kind:       LeafKindEvent,
				Depth:      0,
				Summary:    "test",
				Importance: -0.1,
			},
			wantErr: ErrInvalidImportance,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.leaf.Validate()
			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tt.wantErr)
				}
				if err != tt.wantErr && err.Error() != tt.wantErr.Error() {
					t.Fatalf("expected error %v, got %v", tt.wantErr, err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestTokenASTValidate(t *testing.T) {
	validAST := TokenAST{
		SessionID: "s1",
		BranchID:  "main",
		RootID:    "root-0",
		Leaves: map[string]MemoryLeaf{
			"root-0": {ID: "root-0", Kind: LeafKindRoot, Depth: 3, ChildIDs: []string{"section-0"}, Summary: "root", Importance: 1.0},
			"section-0": {ID: "section-0", Kind: LeafKindSection, Depth: 2, ParentID: "root-0", ChildIDs: []string{"cluster-0"}, Summary: "section", Importance: 0.8},
			"cluster-0": {ID: "cluster-0", Kind: LeafKindCluster, Depth: 1, ParentID: "section-0", ChildIDs: []string{"leaf-0"}, Summary: "cluster", Importance: 0.7},
			"leaf-0": {ID: "leaf-0", Kind: LeafKindEvent, Depth: 0, ParentID: "cluster-0", Summary: "event", EventRefs: []string{"evt-1"}, Importance: 0.5},
		},
	}

	if err := validAST.Validate(); err != nil {
		t.Fatalf("valid AST should pass: %v", err)
	}

	t.Run("empty root fails", func(t *testing.T) {
		ast := TokenAST{SessionID: "s1", BranchID: "main", RootID: "", Leaves: map[string]MemoryLeaf{}}
		if err := ast.Validate(); err == nil {
			t.Fatal("expected error for empty root")
		}
	})

	t.Run("circular ref detected", func(t *testing.T) {
		ast := TokenAST{
			SessionID: "s1",
			BranchID:  "main",
			RootID:    "root-0",
			Leaves: map[string]MemoryLeaf{
				"root-0": {ID: "root-0", Kind: LeafKindRoot, Depth: 3, ChildIDs: []string{"root-0"}, Summary: "circular", Importance: 1.0},
			},
		}
		if err := ast.Validate(); err == nil {
			t.Fatal("expected error for circular ref")
		}
	})

	t.Run("depth/parent mismatch detected", func(t *testing.T) {
		ast := TokenAST{
			SessionID: "s1",
			BranchID:  "main",
			RootID:    "root-0",
			Leaves: map[string]MemoryLeaf{
				"root-0":   {ID: "root-0", Kind: LeafKindRoot, Depth: 3, ChildIDs: []string{"leaf-0"}, Summary: "root", Importance: 1.0},
				"leaf-0":   {ID: "leaf-0", Kind: LeafKindEvent, Depth: 0, ParentID: "root-0", Summary: "wrong parent", Importance: 0.5},
			},
		}
		if err := ast.Validate(); err == nil {
			t.Fatal("expected error for depth/parent mismatch (root depth 3, child depth 0)")
		}
	})
}

func TestGetLeavesByDepth(t *testing.T) {
	ast := TokenAST{
		SessionID: "s1",
		BranchID:  "main",
		RootID:    "root-0",
		Leaves: map[string]MemoryLeaf{
			"root-0":     {ID: "root-0", Kind: LeafKindRoot, Depth: 3, Summary: "r", Importance: 1.0},
			"section-0":  {ID: "section-0", Kind: LeafKindSection, Depth: 2, Summary: "s1", Importance: 0.8},
			"section-1":  {ID: "section-1", Kind: LeafKindSection, Depth: 2, Summary: "s2", Importance: 0.9},
			"cluster-0":  {ID: "cluster-0", Kind: LeafKindCluster, Depth: 1, Summary: "c1", Importance: 0.6},
			"cluster-1":  {ID: "cluster-1", Kind: LeafKindCluster, Depth: 1, Summary: "c2", Importance: 0.7},
			"cluster-2":  {ID: "cluster-2", Kind: LeafKindCluster, Depth: 1, Summary: "c3", Importance: 0.5},
		},
	}

	sections := ast.GetLeavesByDepth(2)
	if len(sections) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(sections))
	}
	// Should be sorted by importance descending
	if sections[0].Importance < sections[1].Importance {
		t.Fatalf("sections not sorted by importance: %.1f < %.1f", sections[0].Importance, sections[1].Importance)
	}

	clusters := ast.GetLeavesByDepth(1)
	if len(clusters) != 3 {
		t.Fatalf("expected 3 clusters, got %d", len(clusters))
	}
}

func TestParseGeminiResponse(t *testing.T) {
	geminiJSON := `{
		"root": {
			"id": "root-0",
			"kind": "root",
			"summary": "Discussion about project architecture",
			"importance": 1.0,
			"children": [
				{
					"id": "section-0",
					"kind": "section",
					"summary": "Planning phase",
					"importance": 0.9,
					"children": [
						{
							"id": "cluster-0",
							"kind": "cluster",
							"summary": "Requirements gathering",
							"tags": ["planning", "requirements"],
							"importance": 0.8,
							"event_refs": ["evt-1", "evt-2"],
							"children": [
								{
									"id": "leaf-0",
									"kind": "event",
									"summary": "User asked about API design",
									"importance": 0.7,
									"event_refs": ["evt-1"]
								},
								{
									"id": "leaf-1",
									"kind": "event",
									"summary": "Agent suggested REST patterns",
									"importance": 0.6,
									"event_refs": ["evt-2"]
								}
							]
						}
					]
				}
			]
		},
		"summary": "The conversation focused on API design decisions for the project",
		"metrics": {
			"themes_identified": 1,
			"events_processed": 2,
			"compression_ratio": 0.85,
			"confidence_score": 0.92
		}
	}`

	ast, err := ParseGeminiResponse([]byte(geminiJSON), "session-1", "main")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if err := ast.Validate(); err != nil {
		t.Fatalf("parsed AST invalid: %v", err)
	}

	if ast.RootID != "root-0" {
		t.Fatalf("expected root-0, got %s", ast.RootID)
	}
	if len(ast.Leaves) != 5 {
		t.Fatalf("expected 5 leaves, got %d", len(ast.Leaves))
	}

	root := ast.Leaves["root-0"]
	if len(root.ChildIDs) != 1 {
		t.Fatalf("root should have 1 child, got %d", len(root.ChildIDs))
	}

	// Check event refs propagation
	if len(ast.EventCoverage) != 2 {
		t.Fatalf("expected 2 event refs in coverage, got %d", len(ast.EventCoverage))
	}
}

func TestResponseSchemaStructure(t *testing.T) {
	schema := ResponseSchema()

	// Verify top-level structure
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema should have properties")
	}
	if _, ok := props["root"]; !ok {
		t.Fatal("schema should have root property")
	}
	if _, ok := props["summary"]; !ok {
		t.Fatal("schema should have summary property")
	}
	if _, ok := props["metrics"]; !ok {
		t.Fatal("schema should have metrics property")
	}

	// Verify schema is valid JSON
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("schema should marshal to JSON: %v", err)
	}
	if !json.Valid(data) {
		t.Fatal("schema should be valid JSON")
	}
}

func TestFibonacciTierStats(t *testing.T) {
	ast := TokenAST{
		SessionID: "s1",
		BranchID:  "main",
		RootID:    "root-0",
		Leaves: map[string]MemoryLeaf{
			"root-0":    {ID: "root-0", Kind: LeafKindRoot, Depth: 3, Summary: "r", Importance: 1.0},
			"sec-0":     {ID: "sec-0", Kind: LeafKindSection, Depth: 2, Summary: "s", Importance: 0.8},
			"sec-1":     {ID: "sec-1", Kind: LeafKindSection, Depth: 2, Summary: "s", Importance: 0.7},
			"c-0":       {ID: "c-0", Kind: LeafKindCluster, Depth: 1, Summary: "c", Importance: 0.6},
			"c-1":       {ID: "c-1", Kind: LeafKindCluster, Depth: 1, Summary: "c", Importance: 0.6},
			"c-2":       {ID: "c-2", Kind: LeafKindCluster, Depth: 1, Summary: "c", Importance: 0.5},
			"l-0":       {ID: "l-0", Kind: LeafKindEvent, Depth: 0, Summary: "e", Importance: 0.4},
			"l-1":       {ID: "l-1", Kind: LeafKindEvent, Depth: 0, Summary: "e", Importance: 0.4},
			"l-2":       {ID: "l-2", Kind: LeafKindEvent, Depth: 0, Summary: "e", Importance: 0.3},
			"l-3":       {ID: "l-3", Kind: LeafKindEvent, Depth: 0, Summary: "e", Importance: 0.3},
			"l-4":       {ID: "l-4", Kind: LeafKindEvent, Depth: 0, Summary: "e", Importance: 0.2},
		},
	}

	stats := ast.GetFibonacciTierStats()
	if len(stats) != MaxASTDepth {
		t.Fatalf("expected %d tier stats, got %d", MaxASTDepth, len(stats))
	}

	// Depth 3 should have 1 leaf (matches target of 1)
	if stats[3].Count != 1 {
		t.Fatalf("depth 3 should have 1 leaf, got %d", stats[3].Count)
	}

	// Depth 2 should have 2 leaves (matches target of 2)
	if stats[2].Count != 2 {
		t.Fatalf("depth 2 should have 2 leaves, got %d", stats[2].Count)
	}
}

func TestApproximateCompressionRatio(t *testing.T) {
	ast := TokenAST{
		Leaves: map[string]MemoryLeaf{
			"root-0": {ID: "root-0", Kind: LeafKindRoot, Depth: 3, Summary: "short summary"},
		},
	}
	ratio := ApproximateCompressionRatio(10000, ast)
	if ratio <= 0 || ratio >= 1.0 {
		t.Fatalf("expected compression ratio between 0 and 1, got %.2f", ratio)
	}

	// Zero raw bytes should return 0
	ratio = ApproximateCompressionRatio(0, ast)
	if ratio != 0 {
		t.Fatalf("expected 0 for zero raw bytes, got %.2f", ratio)
	}
}
