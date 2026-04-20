package preprocess

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// rawExtraction is the shape the fast model returns. We parse into this
// first, then normalize into the typed Extraction. The model won't produce
// schema_version or timestamp — those are injected by the daemon.
type rawExtraction struct {
	Intent          string         `json:"intent"`
	IntentConf      float64        `json:"intent_confidence"`
	Entities        []rawEntity    `json:"entities"`
	Topics          []string       `json:"topics"`
	Sentiment       string         `json:"sentiment"`
	SentimentConf   float64        `json:"sentiment_confidence"`
	Summary         string         `json:"summary"`
	Flags           []string       `json:"flags"`
}

type rawEntity struct {
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Confidence float64 `json:"confidence"`
	MatchType  string  `json:"match_type"`
}

// parseExtraction takes raw JSON bytes from the fast model and produces
// a typed, validated Extraction. Injects schema_version and timestamp.
func parseExtraction(raw []byte) (*Extraction, error) {
	// Strip any markdown fences the model might include despite instructions
	cleaned := stripMarkdownFences(string(raw))

	var parsed rawExtraction
	if err := json.Unmarshal([]byte(cleaned), &parsed); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w (raw: %.200s)", err, cleaned)
	}

	now := time.Now().UTC()

	ext := &Extraction{
		SchemaVersion:    SchemaVersionV1,
		Intent:           IntentClass(parsed.Intent),
		IntentConf:       parsed.IntentConf,
		Entities:         []Entity{},
		Topics:           []TopicTag{},
		Sentiment:        Sentiment(parsed.Sentiment),
		SentimentConf:    parsed.SentimentConf,
		Summary:          truncate(parsed.Summary, 200),
		Flags:            []Flag{},
		SourceLength:     0, // caller sets this
		Timestamp:        now,
	}

	// Normalize entities — null becomes empty slice
	if len(parsed.Entities) > 0 {
		ext.Entities = make([]Entity, 0, len(parsed.Entities))
		for _, re := range parsed.Entities {
			if re.Name == "" {
				continue
			}
			ext.Entities = append(ext.Entities, Entity{
				Name:       strings.TrimSpace(re.Name),
				Type:       EntityType(re.Type),
				Confidence: re.Confidence,
				MatchType:  defaultMatchType(re.MatchType),
			})
		}
	}

	// Normalize topics
	if len(parsed.Topics) > 0 {
		ext.Topics = make([]TopicTag, 0, len(parsed.Topics))
		for _, t := range parsed.Topics {
			tt := TopicTag(t)
			if isValidTopic(tt) {
				ext.Topics = append(ext.Topics, tt)
			}
		}
	}

	// Normalize flags
	if len(parsed.Flags) > 0 {
		ext.Flags = make([]Flag, 0, len(parsed.Flags))
		for _, f := range parsed.Flags {
			ff := Flag(f)
			if isValidFlag(ff) {
				ext.Flags = append(ext.Flags, ff)
			}
		}
	}

	// Validate intent — if the model returns garbage, default to unknown
	if !isValidIntent(ext.Intent) {
		ext.Intent = IntentUnknown
		ext.IntentConf = 0.0
		ext.Flags = append(ext.Flags, FlagUncertain)
	}

	// Validate sentiment — if garbage, default to neutral
	if !isValidSentiment(ext.Sentiment) {
		ext.Sentiment = SentimentNeutral
		ext.SentimentConf = 0.0
	}

	return ext, nil
}

func stripMarkdownFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```json") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	return s
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func defaultMatchType(mt string) string {
	switch mt {
	case "exact", "fuzzy", "inferred":
		return mt
	default:
		return "inferred"
	}
}

func isValidIntent(i IntentClass) bool {
	switch i {
	case IntentQuestion, IntentCommand, IntentStatusUpdate,
		IntentArtifactShare, IntentConversation, IntentCorrection, IntentUnknown:
		return true
	}
	return false
}

func isValidSentiment(s Sentiment) bool {
	switch s {
	case SentimentNeutral, SentimentPositive, SentimentFrustrated, SentimentUrgent:
		return true
	}
	return false
}

func isValidTopic(t TopicTag) bool {
	switch t {
	case TopicCode, TopicDebugging, TopicPlanning, TopicInfra,
		TopicData, TopicReview, TopicMeta, TopicGeneral:
		return true
	}
	return false
}

func isValidFlag(f Flag) bool {
	switch f {
	case FlagUncertain, FlagMultiIntent, FlagAmbiguousEntity,
		FlagLowConfidence, FlagNeedsReview:
		return true
	}
	return false
}
