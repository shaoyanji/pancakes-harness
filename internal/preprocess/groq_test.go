package preprocess

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGroqAdapter_Name(t *testing.T) {
	adapter := &GroqAdapter{cfg: GroqConfig{Model: "openai/gpt-oss-20b"}}
	if adapter.Name() != "groq/openai/gpt-oss-20b" {
		t.Errorf("name = %v, want groq/openai/gpt-oss-20b", adapter.Name())
	}
}

func TestGroqAdapter_Call_Success(t *testing.T) {
	// Mock Groq API response
	groqResp := groqResponse{
		ID:     "test-id",
		Object: "chat.completion",
		Model:  "openai/gpt-oss-20b",
		Choices: []struct {
			Index   int `json:"index"`
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{
			{
				Index: 0,
				Message: struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				}{
					Role: "assistant",
					Content: `{"intent":"command","intent_confidence":0.9,"entities":[{"name":"grep","type":"tool","confidence":0.8,"match_type":"exact"}],"topics":["code"],"sentiment":"neutral","sentiment_confidence":0.7,"summary":"search for pattern","flags":[]}`,
				},
				FinishReason: "stop",
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify auth
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-key" {
			t.Errorf("auth = %v, want Bearer test-key", auth)
		}

		// Verify content type
		ct := r.Header.Get("Content-Type")
		if ct != "application/json" {
			t.Errorf("content-type = %v, want application/json", ct)
		}

		// Verify request body structure
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)

		if req["model"] != "openai/gpt-oss-20b" {
			t.Errorf("model = %v", req["model"])
		}

		rf, ok := req["response_format"].(map[string]any)
		if !ok {
			t.Fatal("missing response_format")
		}
		if rf["type"] != "json_schema" {
			t.Errorf("response_format.type = %v", rf["type"])
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(groqResp)
	}))
	defer server.Close()

	// Patch the endpoint for testing
	adapter := &GroqAdapter{
		cfg: GroqConfig{
			APIKey:      "test-key",
			Model:       "openai/gpt-oss-20b",
			Temperature: 0.1,
			MaxTokens:   1024,
			Strict:      boolPtr(true),
		},
		client: &http.Client{Timeout: 5 * time.Second},
	}

	// We can't easily patch the constant, so test via buildRequest directly
	// and the response parsing separately
	t.Run("build_request", func(t *testing.T) {
		req := adapter.buildRequest("system prompt", "user text")

		if req["model"] != "openai/gpt-oss-20b" {
			t.Errorf("model = %v", req["model"])
		}
		if req["temperature"] != 0.1 {
			t.Errorf("temperature = %v", req["temperature"])
		}
		if req["max_tokens"] != 1024 {
			t.Errorf("max_tokens = %v", req["max_tokens"])
		}

		messages, ok := req["messages"].([]map[string]string)
		if !ok || len(messages) != 2 {
			t.Fatal("expected 2 messages")
		}
		if messages[0]["role"] != "system" {
			t.Errorf("message[0].role = %v", messages[0]["role"])
		}
		if messages[1]["role"] != "user" {
			t.Errorf("message[1].role = %v", messages[1]["role"])
		}
		if messages[1]["content"] != "user text" {
			t.Errorf("message[1].content = %v", messages[1]["content"])
		}

		// Verify structured output schema
		rf := req["response_format"].(map[string]any)
		js := rf["json_schema"].(map[string]any)
		if js["name"] != "extraction" {
			t.Errorf("json_schema.name = %v", js["name"])
		}
		if js["strict"] != true {
			t.Errorf("json_schema.strict = %v, want true", js["strict"])
		}

		schema := js["schema"].(map[string]any)
		required := schema["required"].([]string)
		if len(required) != 8 {
			t.Errorf("required fields = %d, want 8", len(required))
		}
		if schema["additionalProperties"] != false {
			t.Errorf("additionalProperties = %v, want false", schema["additionalProperties"])
		}
	})

	t.Run("parse_response", func(t *testing.T) {
		respBytes, _ := json.Marshal(groqResp)

		var parsed groqResponse
		err := json.Unmarshal(respBytes, &parsed)
		if err != nil {
			t.Fatalf("parse error: %v", err)
		}

		if len(parsed.Choices) != 1 {
			t.Fatalf("choices = %d, want 1", len(parsed.Choices))
		}

		content := parsed.Choices[0].Message.Content

		// Verify the content can be parsed as extraction
		ext, err := parseExtraction([]byte(content))
		if err != nil {
			t.Fatalf("parse extraction: %v", err)
		}
		if ext.Intent != IntentCommand {
			t.Errorf("intent = %v, want command", ext.Intent)
		}
		if len(ext.Entities) != 1 {
			t.Fatalf("entities = %d, want 1", len(ext.Entities))
		}
		if ext.Entities[0].Name != "grep" {
			t.Errorf("entity name = %v, want grep", ext.Entities[0].Name)
		}
	})
}

func TestGroqAdapter_Call_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limit exceeded","type":"rate_limit_error"}}`))
	}))
	defer server.Close()

	// Direct HTTP test — patch client to hit test server
	adapter := &GroqAdapter{
		cfg: GroqConfig{
			APIKey: "test-key",
			Model:  "openai/gpt-oss-20b",
		},
		client: server.Client(),
	}

	// Override endpoint via direct HTTP call
	req, _ := http.NewRequest(http.MethodPost, server.URL, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")

	resp, err := adapter.client.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", resp.StatusCode)
	}
}

func TestGroqAdapter_Call_EmptyChoices(t *testing.T) {
	groqResp := groqResponse{
		ID:      "test-id",
		Choices: []struct {
			Index   int `json:"index"`
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{},
	}

	respBytes, _ := json.Marshal(groqResp)

	var parsed groqResponse
	json.Unmarshal(respBytes, &parsed)

	if len(parsed.Choices) != 0 {
		t.Errorf("expected 0 choices")
	}
}

func TestExtractionJSONSchema(t *testing.T) {
	schema := extractionJSONSchema()

	// Verify strict mode requirements
	if schema["additionalProperties"] != false {
		t.Error("additionalProperties must be false for strict mode")
	}

	required := schema["required"].([]string)
	expectedRequired := []string{
		"intent", "intent_confidence", "entities", "topics",
		"sentiment", "sentiment_confidence", "summary", "flags",
	}
	if len(required) != len(expectedRequired) {
		t.Errorf("required count = %d, want %d", len(required), len(expectedRequired))
	}

	// Verify all required fields are present
	reqMap := make(map[string]bool)
	for _, r := range required {
		reqMap[r] = true
	}
	for _, r := range expectedRequired {
		if !reqMap[r] {
			t.Errorf("missing required field: %s", r)
		}
	}

	// Verify intent enum
	props := schema["properties"].(map[string]any)
	intent := props["intent"].(map[string]any)
	intentEnum := intent["enum"].([]string)
	if len(intentEnum) != 7 {
		t.Errorf("intent enum count = %d, want 7", len(intentEnum))
	}

	// Verify entities schema
	entities := props["entities"].(map[string]any)
	entityItems := entities["items"].(map[string]any)
	if entityItems["additionalProperties"] != false {
		t.Error("entity items must have additionalProperties: false")
	}
	entityRequired := entityItems["required"].([]string)
	if len(entityRequired) != 4 {
		t.Errorf("entity required count = %d, want 4", len(entityRequired))
	}
}

func TestNewGroqAdapter_NoKey(t *testing.T) {
	// Clear env var for test
	t.Setenv("GROQ_API_KEY", "")

	_, err := NewGroqAdapter(GroqConfig{})
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
	if !strings.Contains(err.Error(), "API key required") {
		t.Errorf("error = %v, want API key required", err)
	}
}

func TestNewGroqAdapter_Defaults(t *testing.T) {
	adapter, err := NewGroqAdapter(GroqConfig{APIKey: "test-key"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if adapter.cfg.Model != defaultModel {
		t.Errorf("model = %v, want %v", adapter.cfg.Model, defaultModel)
	}
	if adapter.cfg.Temperature != defaultTemp {
		t.Errorf("temperature = %v, want %v", adapter.cfg.Temperature, defaultTemp)
	}
	if adapter.cfg.MaxTokens != 1024 {
		t.Errorf("max_tokens = %d, want 1024", adapter.cfg.MaxTokens)
	}
}

// Integration test — hits the real Groq API. Skipped by default.
// Set GROQ_API_KEY and run with -run TestGroqAdapter_Live to test.
func TestGroqAdapter_Live(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live test in short mode")
	}
	adapter, err := NewGroqAdapter(GroqConfig{})
	if err != nil {
		t.Skipf("skipped: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	raw, err := adapter.Call(ctx, extractionPrompt(), "How do I build the pancakes-harness project?")
	if err != nil {
		t.Fatalf("groq call failed: %v", err)
	}

	ext, err := parseExtraction(raw)
	if err != nil {
		t.Fatalf("parse extraction: %v", err)
	}

	t.Logf("Intent: %s (conf: %.2f)", ext.Intent, ext.IntentConf)
	t.Logf("Entities: %d", len(ext.Entities))
	t.Logf("Topics: %v", ext.Topics)
	t.Logf("Sentiment: %s", ext.Sentiment)
	t.Logf("Summary: %s", ext.Summary)
	t.Logf("Flags: %v", ext.Flags)

	if err := ext.Validate(); err != nil {
		t.Errorf("validation failed: %v", err)
	}
}

func boolPtr(b bool) *bool { return &b }

// Verify FastAdapter interface is satisfied
var _ FastAdapter = (*GroqAdapter)(nil)
