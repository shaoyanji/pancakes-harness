package preprocess

import (
	"encoding/json"
	"testing"
	"time"
)

func TestExtraction_Validate(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name    string
		e       Extraction
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid extraction",
			e: Extraction{
				SchemaVersion:   SchemaVersionV1,
				Intent:          IntentQuestion,
				IntentConf:      0.85,
				Entities:        []Entity{{Name: "fibtransponder", Type: EntityProject, Confidence: 0.9, MatchType: "exact"}},
				Topics:          []TopicTag{TopicCode},
				Sentiment:       SentimentNeutral,
				SentimentConf:   0.7,
				Summary:         "User asking about fibtransponder build",
				SourceLength:    42,
				Timestamp:       now,
			},
			wantErr: false,
		},
		{
			name: "summary too long",
			e: Extraction{
				SchemaVersion: SchemaVersionV1,
				Intent:        IntentQuestion,
				IntentConf:    0.8,
				Sentiment:     SentimentNeutral,
				SentimentConf: 0.7,
				Summary:       string(make([]byte, 201)),
				Timestamp:     now,
			},
			wantErr: true,
			errMsg:  "summary",
		},
		{
			name: "intent confidence out of range",
			e: Extraction{
				SchemaVersion: SchemaVersionV1,
				Intent:        IntentCommand,
				IntentConf:    1.5,
				Sentiment:     SentimentNeutral,
				SentimentConf: 0.7,
				Timestamp:     now,
			},
			wantErr: true,
			errMsg:  "intent_confidence",
		},
		{
			name: "entity with empty name",
			e: Extraction{
				SchemaVersion: SchemaVersionV1,
				Intent:        IntentCommand,
				IntentConf:    0.9,
				Entities:      []Entity{{Name: "", Type: EntityTool, Confidence: 0.8, MatchType: "fuzzy"}},
				Sentiment:     SentimentNeutral,
				SentimentConf: 0.7,
				Timestamp:     now,
			},
			wantErr: true,
			errMsg:  "entities[0].name",
		},
		{
			name: "entity confidence out of range",
			e: Extraction{
				SchemaVersion: SchemaVersionV1,
				Intent:        IntentCommand,
				IntentConf:    0.9,
				Entities:      []Entity{{Name: "test", Type: EntityTool, Confidence: -0.1, MatchType: "exact"}},
				Sentiment:     SentimentNeutral,
				SentimentConf: 0.7,
				Timestamp:     now,
			},
			wantErr: true,
			errMsg:  "entities[0].confidence",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.e.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr && err != nil {
				ve, ok := err.(*ValidationError)
				if !ok {
					t.Fatalf("expected ValidationError, got %T", err)
				}
				if tt.errMsg != "" && ve.Field != tt.errMsg {
					t.Errorf("expected field %q, got %q", tt.errMsg, ve.Field)
				}
			}
		})
	}
}

func TestExtraction_HasFlag(t *testing.T) {
	e := Extraction{
		Flags: []Flag{FlagUncertain, FlagMultiIntent},
	}
	if !e.HasFlag(FlagUncertain) {
		t.Error("expected HasFlag(FlagUncertain) = true")
	}
	if e.HasFlag(FlagNeedsReview) {
		t.Error("expected HasFlag(FlagNeedsReview) = false")
	}
}

func TestExtraction_ShouldRouteToStrong(t *testing.T) {
	tests := []struct {
		name   string
		e      Extraction
		expect bool
	}{
		{
			name:   "uncertain flag",
			e:      Extraction{Flags: []Flag{FlagUncertain}, IntentConf: 0.9},
			expect: true,
		},
		{
			name:   "multi intent flag",
			e:      Extraction{Flags: []Flag{FlagMultiIntent}, IntentConf: 0.9},
			expect: true,
		},
		{
			name:   "needs review flag",
			e:      Extraction{Flags: []Flag{FlagNeedsReview}, IntentConf: 0.9},
			expect: true,
		},
		{
			name:   "low intent confidence",
			e:      Extraction{IntentConf: 0.3},
			expect: true,
		},
		{
			name:   "confident, no flags",
			e:      Extraction{IntentConf: 0.9},
			expect: false,
		},
		{
			name:   "ambiguous entity but high confidence",
			e:      Extraction{Flags: []Flag{FlagAmbiguousEntity}, IntentConf: 0.9},
			expect: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.e.ShouldRouteToStrong()
			if got != tt.expect {
				t.Errorf("ShouldRouteToStrong() = %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestExtraction_Meta(t *testing.T) {
	now := time.Now().UTC()
	e := Extraction{
		SchemaVersion:    SchemaVersionV1,
		Intent:           IntentCommand,
		IntentConf:       0.92,
		Entities:         []Entity{{Name: "pancakes-harness", Type: EntityProject, Confidence: 0.95, MatchType: "exact"}},
		Topics:           []TopicTag{TopicCode, TopicPlanning},
		Sentiment:        SentimentNeutral,
		SentimentConf:    0.8,
		Summary:          "User wants to add preprocessing to harness",
		Flags:            []Flag{FlagMultiIntent},
		SourceLength:     120,
		Timestamp:        now,
	}

	meta := e.Meta()

	if meta["intent"] != "command" {
		t.Errorf("intent = %v, want command", meta["intent"])
	}
	if meta["intent_confidence"] != 0.92 {
		t.Errorf("intent_confidence = %v, want 0.92", meta["intent_confidence"])
	}
	if meta["sentiment"] != "neutral" {
		t.Errorf("sentiment = %v, want neutral", meta["sentiment"])
	}
	if meta["summary"] != "User wants to add preprocessing to harness" {
		t.Errorf("summary mismatch")
	}
	if meta["source_length"] != 120 {
		t.Errorf("source_length = %v, want 120", meta["source_length"])
	}

	entities, ok := meta["entities"].([]map[string]any)
	if !ok || len(entities) != 1 {
		t.Fatal("expected 1 entity in meta")
	}
	if entities[0]["name"] != "pancakes-harness" {
		t.Errorf("entity name = %v, want pancakes-harness", entities[0]["name"])
	}
	if entities[0]["type"] != "project" {
		t.Errorf("entity type = %v, want project", entities[0]["type"])
	}

	topics, ok := meta["topics"].([]string)
	if !ok || len(topics) != 2 {
		t.Fatal("expected 2 topics in meta")
	}
	if topics[0] != "code" || topics[1] != "planning" {
		t.Errorf("topics = %v, want [code planning]", topics)
	}

	flags, ok := meta["flags"].([]string)
	if !ok || len(flags) != 1 || flags[0] != "multi_intent" {
		t.Errorf("flags = %v, want [multi_intent]", meta["flags"])
	}
}

func TestExtraction_Meta_OmitsEmpty(t *testing.T) {
	e := Extraction{
		SchemaVersion:    SchemaVersionV1,
		Intent:           IntentUnknown,
		IntentConf:       0.1,
		Sentiment:        SentimentNeutral,
		SentimentConf:    0.5,
		SourceLength:     0,
		Timestamp:        time.Now().UTC(),
	}

	meta := e.Meta()

	// Empty slices still project as empty arrays, not omitted
	if _, ok := meta["entities"]; ok {
		t.Error("expected entities to be omitted when nil")
	}
	if _, ok := meta["topics"]; ok {
		t.Error("expected topics to be omitted when nil")
	}
	if _, ok := meta["summary"]; ok {
		t.Error("expected summary to be omitted when empty")
	}
	if _, ok := meta["flags"]; ok {
		t.Error("expected flags to be omitted when nil")
	}
}

func TestParseExtraction_EmptyNotNull(t *testing.T) {
	raw := `{
		"intent": "question",
		"intent_confidence": 0.8,
		"entities": null,
		"topics": [],
		"sentiment": "neutral",
		"sentiment_confidence": 0.7,
		"summary": "test",
		"flags": null
	}`

	ext, err := parseExtraction([]byte(raw))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	if ext.Entities == nil {
		t.Error("entities should be empty slice, not nil")
	}
	if len(ext.Entities) != 0 {
		t.Errorf("entities count = %d, want 0", len(ext.Entities))
	}
	if ext.Topics == nil {
		t.Error("topics should be empty slice, not nil")
	}
	if ext.Flags == nil {
		t.Error("flags should be empty slice, not nil")
	}

	// Verify JSON marshaling produces [] not null
	data, err := json.Marshal(ext)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	jsonStr := string(data)
	if containsStr(jsonStr, `"entities":null`) {
		t.Error("entities marshaled as null, should be []")
	}
	if containsStr(jsonStr, `"flags":null`) {
		t.Error("flags marshaled as null, should be []")
	}
}

func TestRouting_Meta(t *testing.T) {
	now := time.Now().UTC()
	r := Routing{
		SchemaVersion:   SchemaVersionV1,
		Intent:          IntentCommand,
		SuggestedTool:   "grep",
		TargetAgent:     "builder",
		Priority:        "high",
		RequiresContext: true,
		Reasoning:       "User is requesting a code search, grep is the appropriate tool",
		Timestamp:       now,
	}

	meta := r.Meta()

	if meta["intent"] != "command" {
		t.Errorf("intent = %v, want command", meta["intent"])
	}
	if meta["priority"] != "high" {
		t.Errorf("priority = %v, want high", meta["priority"])
	}
	if meta["suggested_tool"] != "grep" {
		t.Errorf("suggested_tool = %v, want grep", meta["suggested_tool"])
	}
	if meta["target_agent"] != "builder" {
		t.Errorf("target_agent = %v, want builder", meta["target_agent"])
	}
	if meta["requires_context"] != true {
		t.Errorf("requires_context = %v, want true", meta["requires_context"])
	}
}

func TestEnvelope_Meta(t *testing.T) {
	now := time.Now().UTC()
	env := Envelope{
		SchemaVersion: SchemaVersionV1,
		Extraction: &Extraction{
			SchemaVersion:    SchemaVersionV1,
			Intent:           IntentQuestion,
			IntentConf:       0.8,
			Sentiment:        SentimentNeutral,
			SentimentConf:    0.7,
			Summary:          "How do I build the project",
			SourceLength:     30,
			Timestamp:        now,
		},
		Routing: &Routing{
			SchemaVersion: SchemaVersionV1,
			Intent:        IntentQuestion,
			Priority:      "medium",
			Reasoning:     "Build question, no tool needed",
			Timestamp:     now,
		},
		Processing: ProcessingMeta{
			FastModelUsed:      true,
			FastModelName:      "gpt-oss-20b",
			FastModelLatencyMs: 180,
			StrongModelUsed:    false,
			TotalMs:            200,
		},
	}

	meta := env.Meta()

	if meta["schema_version"] != SchemaVersionV1 {
		t.Errorf("schema_version = %v", meta["schema_version"])
	}

	extraction, ok := meta["extraction"].(map[string]any)
	if !ok {
		t.Fatal("expected extraction in meta")
	}
	if extraction["intent"] != "question" {
		t.Errorf("extraction.intent = %v, want question", extraction["intent"])
	}

	routing, ok := meta["routing"].(map[string]any)
	if !ok {
		t.Fatal("expected routing in meta")
	}
	if routing["priority"] != "medium" {
		t.Errorf("routing.priority = %v, want medium", routing["priority"])
	}

	processing, ok := meta["processing"].(map[string]any)
	if !ok {
		t.Fatal("expected processing in meta")
	}
	if processing["fast_model_used"] != true {
		t.Errorf("processing.fast_model_used = %v, want true", processing["fast_model_used"])
	}
	if processing["fast_model_name"] != "gpt-oss-20b" {
		t.Errorf("processing.fast_model_name = %v, want gpt-oss-20b", processing["fast_model_name"])
	}
	if processing["total_ms"] != 200 {
		t.Errorf("processing.total_ms = %v, want 200", processing["total_ms"])
	}
}

func TestEnvelope_JSON_RoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	env := Envelope{
		SchemaVersion: SchemaVersionV1,
		Extraction: &Extraction{
			SchemaVersion:    SchemaVersionV1,
			Intent:           IntentStatusUpdate,
			IntentConf:       0.95,
			Entities:         []Entity{{Name: "deploy", Type: EntityTool, Confidence: 0.88, MatchType: "exact"}},
			Topics:           []TopicTag{TopicInfra},
			Sentiment:        SentimentPositive,
			SentimentConf:    0.75,
			Summary:          "Deployment succeeded",
			SourceLength:     20,
			Timestamp:        now,
		},
		Processing: ProcessingMeta{
			FastModelUsed:      true,
			FastModelName:      "gpt-oss-20b",
			FastModelLatencyMs: 120,
			TotalMs:            125,
		},
	}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded Envelope
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.SchemaVersion != SchemaVersionV1 {
		t.Errorf("schema_version = %v", decoded.SchemaVersion)
	}
	if decoded.Extraction == nil {
		t.Fatal("extraction is nil after round-trip")
	}
	if decoded.Extraction.Intent != IntentStatusUpdate {
		t.Errorf("intent = %v, want status_update", decoded.Extraction.Intent)
	}
	if len(decoded.Extraction.Entities) != 1 {
		t.Fatalf("entities count = %d, want 1", len(decoded.Extraction.Entities))
	}
	if decoded.Extraction.Entities[0].Name != "deploy" {
		t.Errorf("entity name = %v, want deploy", decoded.Extraction.Entities[0].Name)
	}
	if decoded.Processing.FastModelName != "gpt-oss-20b" {
		t.Errorf("fast_model_name = %v, want gpt-oss-20b", decoded.Processing.FastModelName)
	}
}

// Golden test: deterministic Meta() output for a canonical extraction.
func TestExtraction_Meta_Golden(t *testing.T) {
	now := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	e := Extraction{
		SchemaVersion:    SchemaVersionV1,
		Intent:           IntentCommand,
		IntentConf:       0.91,
		Entities: []Entity{
			{Name: "pancakes-harness", Type: EntityProject, Confidence: 0.95, MatchType: "exact"},
			{Name: "matt", Type: EntityPerson, Confidence: 0.99, MatchType: "exact"},
		},
		Topics:           []TopicTag{TopicCode, TopicDebugging},
		Sentiment:        SentimentNeutral,
		SentimentConf:    0.85,
		Summary:          "Add preprocessing schema to harness",
		Flags:            []Flag{FlagMultiIntent},
		SourceLength:     200,
		Timestamp:        now,
	}

	meta := e.Meta()

	// Verify exact structure — this is the golden contract.
	if meta["intent"] != "command" {
		t.Errorf("golden: intent = %v", meta["intent"])
	}
	if meta["intent_confidence"] != 0.91 {
		t.Errorf("golden: intent_confidence = %v", meta["intent_confidence"])
	}
	if meta["sentiment"] != "neutral" {
		t.Errorf("golden: sentiment = %v", meta["sentiment"])
	}
	if meta["summary"] != "Add preprocessing schema to harness" {
		t.Errorf("golden: summary = %v", meta["summary"])
	}
	if meta["source_length"] != 200 {
		t.Errorf("golden: source_length = %v", meta["source_length"])
	}

	entities := meta["entities"].([]map[string]any)
	if len(entities) != 2 {
		t.Fatalf("golden: entities count = %d", len(entities))
	}
	if entities[0]["name"] != "pancakes-harness" || entities[0]["type"] != "project" {
		t.Errorf("golden: entity[0] = %v", entities[0])
	}
	if entities[1]["name"] != "matt" || entities[1]["type"] != "person" {
		t.Errorf("golden: entity[1] = %v", entities[1])
	}

	topics := meta["topics"].([]string)
	if len(topics) != 2 || topics[0] != "code" || topics[1] != "debugging" {
		t.Errorf("golden: topics = %v", topics)
	}

	flags := meta["flags"].([]string)
	if len(flags) != 1 || flags[0] != "multi_intent" {
		t.Errorf("golden: flags = %v", flags)
	}
}
