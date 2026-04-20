package preprocess

import "testing"

func TestExtractionPrompt(t *testing.T) {
	prompt := extractionPrompt()

	if prompt == "" {
		t.Fatal("extraction prompt is empty")
	}

	// Verify key schema elements are in the prompt
	required := []string{
		"intent",
		"intent_confidence",
		"entities",
		"topics",
		"sentiment",
		"sentiment_confidence",
		"summary",
		"flags",
		"uncertain",
		"multi_intent",
		"ambiguous_entity",
	}
	for _, r := range required {
		if !containsStr(prompt, r) {
			t.Errorf("prompt missing required element: %s", r)
		}
	}

	// Verify intent enum values are present
	intents := []string{"question", "command", "status_update", "artifact_share", "conversation", "correction", "unknown"}
	for _, intent := range intents {
		if !containsStr(prompt, intent) {
			t.Errorf("prompt missing intent value: %s", intent)
		}
	}

	// Verify entity types are present
	entityTypes := []string{"project", "person", "tool", "file", "session", "branch"}
	for _, et := range entityTypes {
		if !containsStr(prompt, et) {
			t.Errorf("prompt missing entity type: %s", et)
		}
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && searchStr(s, sub)
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
