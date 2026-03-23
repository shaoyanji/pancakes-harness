package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type OllamaConfig struct {
	Endpoint string
	Model    string
	Timeout  time.Duration
}

type OllamaAdapter struct {
	cfg    OllamaConfig
	client *http.Client
}

func NewOllamaAdapter(cfg OllamaConfig) *OllamaAdapter {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &OllamaAdapter{
		cfg:    cfg,
		client: &http.Client{Timeout: timeout},
	}
}

func (a *OllamaAdapter) Name() string { return "ollama" }

func (a *OllamaAdapter) StatelessCall(ctx context.Context, req Request) ([]byte, error) {
	endpoint := strings.TrimSpace(a.cfg.Endpoint)
	modelName := strings.TrimSpace(a.cfg.Model)
	if endpoint == "" {
		return nil, fmt.Errorf("%w: ollama endpoint is required", ErrAdapterCallFailed)
	}
	if modelName == "" {
		return nil, fmt.Errorf("%w: ollama model is required", ErrAdapterCallFailed)
	}
	body, err := buildOllamaChatRequest(modelName, req.Packet)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAdapterCallFailed, err)
	}

	url := strings.TrimRight(endpoint, "/") + "/api/chat"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAdapterCallFailed, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAdapterCallFailed, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAdapterCallFailed, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: status=%d", ErrAdapterCallFailed, resp.StatusCode)
	}
	raw, err := extractOllamaMessageContent(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAdapterCallFailed, err)
	}
	return raw, nil
}

func buildOllamaChatRequest(modelName string, packet []byte) ([]byte, error) {
	type message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type schemaProperty struct {
		Type string   `json:"type"`
		Enum []string `json:"enum,omitempty"`
	}
	type schemaFormat struct {
		Type                 string                    `json:"type"`
		Properties           map[string]schemaProperty `json:"properties"`
		Required             []string                  `json:"required"`
		AdditionalProperties bool                      `json:"additionalProperties"`
	}
	type request struct {
		Model    string       `json:"model"`
		Stream   bool         `json:"stream"`
		Think    bool         `json:"think"`
		Messages []message    `json:"messages"`
		Format   schemaFormat `json:"format"`
	}

	userContent := "Respond with JSON matching the schema only. Input packet:\n" + string(packet)
	wire := request{
		Model:  modelName,
		Stream: false,
		Think:  false,
		Messages: []message{
			{
				Role: "system",
				Content: "You are a stateless model adapter.\n" +
					"The packet is internal context. Use it only to answer the latest user request naturally.\n" +
					"Do not summarize the packet.\n" +
					"Do not mention session IDs, branch IDs, working_set, event IDs, or packet structure unless explicitly asked.\n" +
					"Return exactly one JSON object and nothing else.\n" +
					"No prose outside JSON. No markdown. No code fences. No extra keys.\n" +
					"The object must contain exactly keys: decision and answer.\n" +
					"decision must be \"answer\".\n" +
					"Examples:\n" +
					"- latest user request: \"hello\" -> {\"decision\":\"answer\",\"answer\":\"Hello! How can I help you today?\"}\n" +
					"- latest user request: \"what is 2+2?\" -> {\"decision\":\"answer\",\"answer\":\"2+2 is 4.\"}",
			},
			{
				Role:    "user",
				Content: userContent,
			},
		},
		Format: schemaFormat{
			Type: "object",
			Properties: map[string]schemaProperty{
				"decision": {Type: "string", Enum: []string{"answer"}},
				"answer":   {Type: "string"},
			},
			Required:             []string{"decision", "answer"},
			AdditionalProperties: false,
		},
	}
	return json.Marshal(wire)
}

func extractOllamaMessageContent(payload []byte) ([]byte, error) {
	type response struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	var decoded response
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("invalid ollama response json: preview=%q", previewContent(string(payload)))
	}
	content := strings.TrimSpace(decoded.Message.Content)
	if content == "" {
		return nil, fmt.Errorf("missing ollama message content: preview=%q", previewContent(string(payload)))
	}
	if !json.Valid([]byte(content)) {
		return nil, fmt.Errorf("ollama content is not valid json: preview=%q", previewContent(content))
	}
	return []byte(content), nil
}

func previewContent(in string) string {
	cleaned := strings.TrimSpace(in)
	cleaned = strings.ReplaceAll(cleaned, "\n", " ")
	cleaned = strings.ReplaceAll(cleaned, "\r", " ")
	cleaned = strings.ReplaceAll(cleaned, "\t", " ")
	if len(cleaned) > 160 {
		return cleaned[:160] + "..."
	}
	return cleaned
}
