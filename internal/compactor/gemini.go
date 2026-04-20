package compactor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"pancakes-harness/internal/eventlog"
)

// GeminiConfig configures the Gemini compaction adapter.
type GeminiConfig struct {
	// API endpoint. Default: https://generativelanguage.googleapis.com/v1beta
	Endpoint string

	// API key or OAuth bearer token (one required).
	APIKey      string
	BearerToken string

	// Model to use. Default: gemini-2.5-flash
	Model string

	// Thinking budget in tokens. 0 = off. Recommended: 2048–4096 for compaction.
	ThinkingBudget int32

	// Temperature for compaction. Default: 0.7 (lower = more consistent)
	Temperature float32

	// Request timeout. Default: 120s (compaction can take a while on large inputs)
	Timeout time.Duration

	// HTTP client override (for testing or custom TLS)
	HTTPClient *http.Client
}

func (c *GeminiConfig) defaults() {
	if c.Endpoint == "" {
		c.Endpoint = "https://generativelanguage.googleapis.com/v1beta"
	}
	if c.Model == "" {
		c.Model = "gemini-2.5-flash"
	}
	if c.ThinkingBudget == 0 {
		c.ThinkingBudget = 2048
	}
	if c.Temperature == 0 {
		c.Temperature = 0.7
	}
	if c.Timeout == 0 {
		c.Timeout = 120 * time.Second
	}
}

// CompactionResult holds the raw response from Gemini plus parsed metrics.
type CompactionResult struct {
	RawJSON      []byte
	SessionID    string
	BranchID     string
	InputTokens  int
	OutputTokens int
	Latency      time.Duration
}

// GeminiAdapter calls the Gemini API with structured output (responseSchema)
// to produce MemoryLeaflet ASTs from full event histories.
type GeminiAdapter struct {
	cfg    GeminiConfig
	client *http.Client
}

// NewGeminiAdapter creates a compaction adapter for Gemini.
func NewGeminiAdapter(cfg GeminiConfig) *GeminiAdapter {
	cfg.defaults()
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &GeminiAdapter{cfg: cfg, client: client}
}

// Name returns the adapter identifier.
func (a *GeminiAdapter) Name() string { return "gemini-compactor" }

// Compact sends the full event list to Gemini with a responseSchema and
// returns the raw structured JSON output.
func (a *GeminiAdapter) Compact(ctx context.Context, events []eventlog.SerializedEvent, sessionID, branchID string) (CompactionResult, error) {
	started := time.Now()

	systemPrompt, userPrompt := BuildCompactionPrompt(events, branchID)

	reqBody := a.buildRequest(systemPrompt, userPrompt)

	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return CompactionResult{}, fmt.Errorf("marshal gemini request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/models/%s:generateContent", a.cfg.Endpoint, a.cfg.Model)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqJSON))
	if err != nil {
		return CompactionResult{}, fmt.Errorf("create http request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	// Auth: prefer bearer token, fallback to API key header
	if a.cfg.BearerToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.cfg.BearerToken)
	} else if a.cfg.APIKey != "" {
		httpReq.Header.Set("x-goog-api-key", a.cfg.APIKey)
	}

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return CompactionResult{}, fmt.Errorf("gemini request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return CompactionResult{}, fmt.Errorf("read gemini response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return CompactionResult{}, fmt.Errorf("gemini API error (status %d): %s", resp.StatusCode, truncate(string(body), 500))
	}

	// Parse the Gemini API response envelope to extract the model's JSON output
	outputJSON, inputTokens, outputTokens, err := extractGeminiContent(body)
	if err != nil {
		return CompactionResult{}, err
	}

	return CompactionResult{
		RawJSON:      outputJSON,
		SessionID:    sessionID,
		BranchID:     branchID,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		Latency:      time.Since(started),
	}, nil
}

// CompactWithCustomPrompts calls Gemini with caller-supplied prompts.
// Used by the merge pass where the prompt content differs from the standard chunk compaction.
func (a *GeminiAdapter) CompactWithCustomPrompts(
	ctx context.Context,
	systemPrompt, userPrompt,
	sessionID, branchID string,
) (rawJSON []byte, inputTokens, outputTokens int, latency time.Duration, err error) {
	started := time.Now()

	reqBody := a.buildRequest(systemPrompt, userPrompt)
	reqJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("marshal merge request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/models/%s:generateContent", a.cfg.Endpoint, a.cfg.Model)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqJSON))
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("create merge request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if a.cfg.BearerToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+a.cfg.BearerToken)
	} else if a.cfg.APIKey != "" {
		httpReq.Header.Set("x-goog-api-key", a.cfg.APIKey)
	}

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("merge request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, 0, 0, fmt.Errorf("read merge response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, 0, 0, 0, fmt.Errorf("merge API error (status %d): %s", resp.StatusCode, truncate(string(body), 500))
	}

	outputJSON, inputTokens, outputTokens, err := extractGeminiContent(body)
	if err != nil {
		return nil, 0, 0, 0, err
	}

	return outputJSON, inputTokens, outputTokens, time.Since(started), nil
}

// buildRequest constructs the Gemini generateContent request with responseSchema.
func (a *GeminiAdapter) buildRequest(systemPrompt, userPrompt string) map[string]any {
	contents := []map[string]any{
		{
			"role": "user",
			"parts": []map[string]any{
				{"text": userPrompt},
			},
		},
	}

	generationConfig := map[string]any{
		"temperature":        a.cfg.Temperature,
		"response_mime_type": "application/json",
		"response_schema":    ResponseSchema(),
	}
	if a.cfg.ThinkingBudget > 0 {
		generationConfig["thinking_config"] = map[string]any{
			"thinking_budget": a.cfg.ThinkingBudget,
		}
	}

	return map[string]any{
		"system_instruction": map[string]any{
			"parts": []map[string]any{
				{"text": systemPrompt},
			},
		},
		"contents":          contents,
		"generation_config": generationConfig,
	}
}

// extractGeminiContent pulls the model's JSON text from the Gemini API envelope.
// Gemini returns: { "candidates": [{ "content": { "parts": [{ "text": "..." }] } }] }
// Plus usage metadata with token counts.
func extractGeminiContent(apiResponse []byte) (outputJSON []byte, inputTokens, outputTokens int, err error) {
	var envelope struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}

	if err = json.Unmarshal(apiResponse, &envelope); err != nil {
		return nil, 0, 0, fmt.Errorf("parse gemini envelope: %w", err)
	}

	if len(envelope.Candidates) == 0 {
		return nil, 0, 0, fmt.Errorf("gemini returned no candidates")
	}

	candidate := envelope.Candidates[0]
	if candidate.FinishReason != "STOP" && candidate.FinishReason != "" {
		return nil, 0, 0, fmt.Errorf("gemini finish reason: %s", candidate.FinishReason)
	}

	// Concatenate all text parts (Gemini may split across parts with thinking)
	var fullText string
	for _, part := range candidate.Content.Parts {
		fullText += part.Text
	}

	if fullText == "" {
		return nil, 0, 0, fmt.Errorf("gemini returned empty text content")
	}

	inputTokens = envelope.UsageMetadata.PromptTokenCount
	outputTokens = envelope.UsageMetadata.CandidatesTokenCount

	return []byte(fullText), inputTokens, outputTokens, nil
}
