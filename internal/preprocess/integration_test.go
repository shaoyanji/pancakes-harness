//go:build integration

package preprocess

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestGroqAdapter_Messages(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	adapter, err := NewGroqAdapter(GroqConfig{})
	if err != nil {
		t.Skipf("skipped: %v", err)
	}

	messages := []struct {
		name string
		text string
	}{
		{"question", "How do I run the test suite for pancakes-harness?"},
		{"command", "Deploy the staging environment and restart the nginx proxy"},
		{"status_update", "Build succeeded on CI, all 47 tests passing. Ready for review."},
		{"frustrated", "This keeps crashing and I've been debugging for 3 hours. Nothing works."},
		{"multi_intent", "Can you check if the deploy worked and also what happened with the memory compaction bug?"},
		{"artifact_share", "Here's the patch for the event spine issue: diff --git a/internal/eventlog/kinds.go"},
		{"correction", "No wait, I meant the other branch. Use feat/dilation-tree not main."},
	}

	for _, m := range messages {
		t.Run(m.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			raw, err := adapter.Call(ctx, extractionPrompt(), m.text)
			if err != nil {
				t.Fatalf("groq call failed: %v", err)
			}

			ext, err := parseExtraction(raw)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if err := ext.Validate(); err != nil {
				t.Errorf("validation failed: %v", err)
			}

			rawJSON, _ := json.MarshalIndent(map[string]any{
				"intent":              string(ext.Intent),
				"intent_confidence":   ext.IntentConf,
				"sentiment":           string(ext.Sentiment),
				"sentiment_confidence": ext.SentimentConf,
				"topics":              ext.Topics,
				"entities":            ext.Entities,
				"summary":             ext.Summary,
				"flags":               ext.Flags,
			}, "", "  ")
			t.Logf("\n%s", string(rawJSON))
		})
	}
}

func TestGroqAdapter_BestEffort_MultiIntent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	strict := false
	adapter, err := NewGroqAdapter(GroqConfig{Strict: &strict})
	if err != nil {
		t.Skipf("skipped: %v", err)
	}

	messages := []struct {
		name string
		text string
	}{
		{"compound", "Can you check if the deploy worked and also what happened with the memory compaction bug?"},
		{"triple", "First restart nginx, then check the logs, and also tell me why CI is red."},
		{"correction_and_question", "Actually use feat/dilation-tree not main. Also when is the next release?"},
	}

	for _, m := range messages {
		t.Run(m.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			raw, err := adapter.Call(ctx, extractionPrompt(), m.text)
			if err != nil {
				t.Fatalf("groq call failed: %v", err)
			}

			ext, err := parseExtraction(raw)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}

			rawJSON, _ := json.MarshalIndent(map[string]any{
				"intent":              string(ext.Intent),
				"intent_confidence":   ext.IntentConf,
				"sentiment":           string(ext.Sentiment),
				"topics":              ext.Topics,
				"entities":            ext.Entities,
				"summary":             ext.Summary,
				"flags":               ext.Flags,
				"should_route_strong": ext.ShouldRouteToStrong(),
			}, "", "  ")
			t.Logf("\n%s", string(rawJSON))

			// Best-effort mode should surface flags on ambiguous messages
			if m.name == "triple" && !ext.HasFlag(FlagMultiIntent) {
				t.Logf("WARNING: expected multi_intent flag on triple-intent message")
			}
		})
	}
}
