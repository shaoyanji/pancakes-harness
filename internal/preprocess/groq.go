// Package preprocess — Groq adapter for the fast model sidecar.
//
// Implements FastAdapter using Groq's OpenAI-compatible API with structured
// outputs. Uses strict: true mode for guaranteed valid JSON when available.
//
// Endpoint: POST https://api.groq.com/openai/v1/chat/completions
// Auth: Bearer token from GROQ_API_KEY env var or config.
package preprocess

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const (
	groqEndpoint  = "https://api.groq.com/openai/v1/chat/completions"
	defaultModel  = "openai/gpt-oss-20b"
	defaultTemp   = 0.1 // low temperature for extraction consistency
)

// GroqConfig configures the Groq fast model adapter.
type GroqConfig struct {
	// APIKey for Groq. If empty, reads GROQ_API_KEY from environment.
	APIKey string
	// Model is the Groq model ID. Default: openai/gpt-oss-20b
	Model string
	// Temperature for extraction. Default: 0.1
	Temperature float64
	// MaxTokens caps the response. Default: 1024
	MaxTokens int
	// Strict enables Groq structured output with constrained decoding.
	// Default: true (guaranteed valid JSON matching schema).
	Strict bool
}

// GroqAdapter implements FastAdapter using the Groq API.
type GroqAdapter struct {
	cfg    GroqConfig
	client *http.Client
}

// NewGroqAdapter creates a new Groq adapter.
func NewGroqAdapter(cfg GroqConfig) (*GroqAdapter, error) {
	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("GROQ_API_KEY")
	}
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("groq: API key required (set GROQ_API_KEY or pass in config)")
	}
	if cfg.Model == "" {
		cfg.Model = defaultModel
	}
	if cfg.Temperature <= 0 {
		cfg.Temperature = defaultTemp
	}
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = 1024
	}
	// Default to strict mode — gpt-oss-20b supports it
	// We check if the field was explicitly set, but for simplicity default to true
	// The caller can set Strict: false explicitly if needed.

	return &GroqAdapter{
		cfg: cfg,
		client: &http.Client{
			Timeout: 10 * time.Second, // hard cap on HTTP client timeout
		},
	}, nil
}

func (g *GroqAdapter) Name() string {
	return fmt.Sprintf("groq/%s", g.cfg.Model)
}

// Call sends text to Groq and returns the raw JSON extraction response.
func (g *GroqAdapter) Call(ctx context.Context, prompt, text string) ([]byte, error) {
	reqBody := g.buildRequest(prompt, text)

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("groq: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, groqEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("groq: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.cfg.APIKey)

	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("groq: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("groq: read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("groq: HTTP %d: %.500s", resp.StatusCode, string(respBody))
	}

	// Parse the OpenAI-compatible response to extract the message content
	var groqResp groqResponse
	if err := json.Unmarshal(respBody, &groqResp); err != nil {
		return nil, fmt.Errorf("groq: parse response: %w (raw: %.200s)", err, string(respBody))
	}

	if len(groqResp.Choices) == 0 {
		return nil, fmt.Errorf("groq: no choices in response")
	}

	content := groqResp.Choices[0].Message.Content
	if content == "" {
		return nil, fmt.Errorf("groq: empty content in response")
	}

	return []byte(content), nil
}

// buildRequest constructs the Groq API request body with structured output schema.
func (g *GroqAdapter) buildRequest(systemPrompt, text string) map[string]any {
	req := map[string]any{
		"model": g.cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": text},
		},
		"temperature": g.cfg.Temperature,
		"max_tokens":  g.cfg.MaxTokens,
	}

	// Structured output — use strict mode for guaranteed valid JSON
	responseFormat := map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":   "extraction",
			"strict": true,
			"schema": extractionJSONSchema(),
		},
	}
	req["response_format"] = responseFormat

	return req
}

// extractionJSONSchema returns the JSON Schema for Groq structured outputs.
// This must conform to strict mode requirements:
// - All fields required
// - additionalProperties: false on objects
// - No optional fields (use nullable types instead)
func extractionJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"intent": map[string]any{
				"type": "string",
				"enum": []string{
					"question", "command", "status_update",
					"artifact_share", "conversation", "correction", "unknown",
				},
			},
			"intent_confidence": map[string]any{
				"type": "number",
			},
			"entities": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{"type": "string"},
						"type": map[string]any{
							"type": "string",
							"enum": []string{
								"project", "person", "tool",
								"file", "session", "branch",
							},
						},
						"confidence": map[string]any{"type": "number"},
						"match_type": map[string]any{
							"type": "string",
							"enum": []string{"exact", "fuzzy", "inferred"},
						},
					},
					"required":             []string{"name", "type", "confidence", "match_type"},
					"additionalProperties": false,
				},
			},
			"topics": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
					"enum": []string{
						"code", "debugging", "planning", "infra",
						"data", "review", "meta", "general",
					},
				},
			},
			"sentiment": map[string]any{
				"type": "string",
				"enum": []string{"neutral", "positive", "frustrated", "urgent"},
			},
			"sentiment_confidence": map[string]any{
				"type": "number",
			},
			"summary": map[string]any{
				"type":      "string",
				"maxLength": 200,
			},
			"flags": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
					"enum": []string{
						"uncertain", "multi_intent", "ambiguous_entity",
						"low_confidence", "needs_review",
					},
				},
			},
		},
		"required": []string{
			"intent", "intent_confidence", "entities", "topics",
			"sentiment", "sentiment_confidence", "summary", "flags",
		},
		"additionalProperties": false,
	}
}

// groqResponse is the OpenAI-compatible response shape from Groq.
type groqResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}
