// Package preprocess defines the golden schema for two-tier input preprocessing.
//
// Layer 1 — Extraction: produced by a fast model (e.g. 20B @ 1000 tok/s).
// Pure enrichment — entities, topics, intent classification, sentiment.
// Tight schema: enums over free text, confidence on every classification,
// escape hatch flags for uncertain output. Safe to run on every ingress.
//
// Layer 2 — Routing: produced by a strong model (or by the harness directly).
// Decisions — which tool, which agent, what priority. The strong model sees
// the Extraction result plus the raw input and decides what should happen.
//
// The Extraction layer is the "golden schema" — it is the contract between
// the fast pre-processor and the rest of the harness. It maps cleanly onto
// the event spine as a new durable kind.
package preprocess

import "time"

// SchemaVersion is the canonical version for extraction/routing records.
const SchemaVersionV1 = "preprocess.v1"

// --- Layer 1: Extraction (fast model output) ---

// IntentClass is the bounded set of ingress intents.
type IntentClass string

const (
	IntentQuestion      IntentClass = "question"
	IntentCommand        IntentClass = "command"
	IntentStatusUpdate   IntentClass = "status_update"
	IntentArtifactShare  IntentClass = "artifact_share"
	IntentConversation   IntentClass = "conversation"
	IntentCorrection     IntentClass = "correction"
	IntentUnknown        IntentClass = "unknown"
)

// EntityType is the bounded set of extractable entity types.
type EntityType string

const (
	EntityProject EntityType = "project"
	EntityPerson  EntityType = "person"
	EntityTool    EntityType = "tool"
	EntityFile    EntityType = "file"
	EntitySession EntityType = "session"
	EntityBranch  EntityType = "branch"
)

// TopicTag is the bounded set of topic classifications.
type TopicTag string

const (
	TopicCode        TopicTag = "code"
	TopicDebugging   TopicTag = "debugging"
	TopicPlanning    TopicTag = "planning"
	TopicInfra       TopicTag = "infra"
	TopicData        TopicTag = "data"
	TopicReview      TopicTag = "review"
	TopicMeta        TopicTag = "meta"
	TopicGeneral     TopicTag = "general"
)

// Sentiment is the bounded set of tone classifications.
type Sentiment string

const (
	SentimentNeutral   Sentiment = "neutral"
	SentimentPositive  Sentiment = "positive"
	SentimentFrustrated Sentiment = "frustrated"
	SentimentUrgent    Sentiment = "urgent"
)

// Flag is an escape hatch for the fast model to signal uncertainty.
type Flag string

const (
	FlagUncertain       Flag = "uncertain"
	FlagMultiIntent     Flag = "multi_intent"
	FlagAmbiguousEntity Flag = "ambiguous_entity"
	FlagLowConfidence   Flag = "low_confidence"
	FlagNeedsReview     Flag = "needs_review"
)

// Entity is a single extracted entity with type and confidence.
type Entity struct {
	Name       string     `json:"name"`
	Type       EntityType `json:"type"`
	Confidence float64    `json:"confidence"` // 0.0–1.0
	MatchType  string     `json:"match_type"` // "exact" | "fuzzy" | "inferred"
}

// Extraction is the fast model's structured enrichment of an ingress message.
// Every field is typed or enumerated. The only free-text field (Summary) is
// length-bounded. This is the golden schema.
type Extraction struct {
	SchemaVersion string       `json:"schema_version"`
	Intent        IntentClass  `json:"intent"`
	IntentConf    float64      `json:"intent_confidence"`
	Entities      []Entity     `json:"entities"`
	Topics        []TopicTag   `json:"topics"`
	Sentiment     Sentiment    `json:"sentiment"`
	SentimentConf float64      `json:"sentiment_confidence"`
	Summary       string       `json:"summary"` // max 200 chars
	Flags         []Flag       `json:"flags,omitempty"`
	SourceLength  int          `json:"source_length"` // original message char count
	Timestamp     time.Time    `json:"timestamp"`
}

// Validate checks structural constraints on the extraction.
func (e *Extraction) Validate() error {
	if len(e.Summary) > 200 {
		return &ValidationError{Field: "summary", Message: "exceeds 200 char limit"}
	}
	if e.IntentConf < 0 || e.IntentConf > 1 {
		return &ValidationError{Field: "intent_confidence", Message: "must be 0.0–1.0"}
	}
	if e.SentimentConf < 0 || e.SentimentConf > 1 {
		return &ValidationError{Field: "sentiment_confidence", Message: "must be 0.0–1.0"}
	}
	for i, ent := range e.Entities {
		if ent.Confidence < 0 || ent.Confidence > 1 {
			return &ValidationError{Field: "entities[" + itoa(i) + "].confidence", Message: "must be 0.0–1.0"}
		}
		if ent.Name == "" {
			return &ValidationError{Field: "entities[" + itoa(i) + "].name", Message: "required"}
		}
	}
	return nil
}

// HasFlag returns true if the extraction carries a specific flag.
func (e *Extraction) HasFlag(f Flag) bool {
	for _, flag := range e.Flags {
		if flag == f {
			return true
		}
	}
	return false
}

// ShouldRouteToStrong returns true if the extraction suggests the input
// should be handled by the strong model rather than processed directly.
func (e *Extraction) ShouldRouteToStrong() bool {
	return e.HasFlag(FlagUncertain) ||
		e.HasFlag(FlagMultiIntent) ||
		e.HasFlag(FlagNeedsReview) ||
		e.IntentConf < 0.5
}

// Meta projects the extraction into event spine metadata.
func (e *Extraction) Meta() map[string]any {
	meta := map[string]any{
		"schema_version":    e.SchemaVersion,
		"intent":            string(e.Intent),
		"intent_confidence": e.IntentConf,
		"sentiment":         string(e.Sentiment),
		"sentiment_confidence": e.SentimentConf,
		"source_length":     e.SourceLength,
	}
	if len(e.Entities) > 0 {
		entities := make([]map[string]any, 0, len(e.Entities))
		for _, ent := range e.Entities {
			entities = append(entities, map[string]any{
				"name":       ent.Name,
				"type":       string(ent.Type),
				"confidence": ent.Confidence,
				"match_type": ent.MatchType,
			})
		}
		meta["entities"] = entities
	}
	if len(e.Topics) > 0 {
		topics := make([]string, 0, len(e.Topics))
		for _, t := range e.Topics {
			topics = append(topics, string(t))
		}
		meta["topics"] = topics
	}
	if e.Summary != "" {
		meta["summary"] = e.Summary
	}
	if len(e.Flags) > 0 {
		flags := make([]string, 0, len(e.Flags))
		for _, f := range e.Flags {
			flags = append(flags, string(f))
		}
		meta["flags"] = flags
	}
	return meta
}

// --- Layer 2: Routing (strong model output) ---

// Routing is the strong model's decision about what should happen with the input.
// It is produced after the Extraction layer and the harness's store/memory query.
type Routing struct {
	SchemaVersion   string      `json:"schema_version"`
	Intent          IntentClass `json:"intent"`          // confirmed or revised intent
	SuggestedTool   string      `json:"suggested_tool,omitempty"`
	TargetAgent     string      `json:"target_agent,omitempty"`
	Priority        string      `json:"priority"`        // "low" | "medium" | "high"
	RequiresContext bool        `json:"requires_context"`
	Reasoning       string      `json:"reasoning"` // why this routing was chosen
	Timestamp       time.Time   `json:"timestamp"`
}

// Meta projects the routing into event spine metadata.
func (r *Routing) Meta() map[string]any {
	meta := map[string]any{
		"schema_version":    r.SchemaVersion,
		"intent":            string(r.Intent),
		"priority":          r.Priority,
		"requires_context":  r.RequiresContext,
		"reasoning":         r.Reasoning,
	}
	if r.SuggestedTool != "" {
		meta["suggested_tool"] = r.SuggestedTool
	}
	if r.TargetAgent != "" {
		meta["target_agent"] = r.TargetAgent
	}
	return meta
}

// --- Combined Envelope ---

// Envelope pairs both layers for the event spine. The fast model produces
// the Extraction; the harness queries memory; the strong model produces
// the Routing. All three steps are recorded as a single durable record.
type Envelope struct {
	SchemaVersion string      `json:"schema_version"`
	Extraction    *Extraction `json:"extraction"`
	Routing       *Routing    `json:"routing,omitempty"`
	// ProcessingMetadata records the two-tier pipeline execution details.
	Processing ProcessingMeta `json:"processing"`
}

// ProcessingMeta records how the envelope was produced.
type ProcessingMeta struct {
	FastModelUsed    bool   `json:"fast_model_used"`
	FastModelName    string `json:"fast_model_name,omitempty"`
	FastModelLatencyMs int  `json:"fast_model_latency_ms,omitempty"`
	StrongModelUsed  bool   `json:"strong_model_used"`
	StrongModelName  string `json:"strong_model_name,omitempty"`
	MemoryQueryMs    int    `json:"memory_query_ms,omitempty"`
	TotalMs          int    `json:"total_ms"`
}

// Meta projects the full envelope into event spine metadata.
func (env *Envelope) Meta() map[string]any {
	meta := map[string]any{
		"schema_version": env.SchemaVersion,
		"processing": map[string]any{
			"fast_model_used":   env.Processing.FastModelUsed,
			"strong_model_used": env.Processing.StrongModelUsed,
			"total_ms":          env.Processing.TotalMs,
		},
	}
	if env.Extraction != nil {
		meta["extraction"] = env.Extraction.Meta()
	}
	if env.Routing != nil {
		meta["routing"] = env.Routing.Meta()
	}
	if env.Processing.FastModelName != "" {
		meta["processing"].(map[string]any)["fast_model_name"] = env.Processing.FastModelName
	}
	if env.Processing.FastModelLatencyMs > 0 {
		meta["processing"].(map[string]any)["fast_model_latency_ms"] = env.Processing.FastModelLatencyMs
	}
	if env.Processing.StrongModelName != "" {
		meta["processing"].(map[string]any)["strong_model_name"] = env.Processing.StrongModelName
	}
	if env.Processing.MemoryQueryMs > 0 {
		meta["processing"].(map[string]any)["memory_query_ms"] = env.Processing.MemoryQueryMs
	}
	return meta
}

// --- Validation ---

type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return "preprocess: " + e.Field + ": " + e.Message
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	// simple int to string for validation messages
	buf := [20]byte{}
	pos := len(buf)
	n := i
	if n < 0 {
		n = -n
	}
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
