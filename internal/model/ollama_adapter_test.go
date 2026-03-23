package model

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaStructuredResponsePassesValidation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":"{\"decision\":\"answer\",\"answer\":\"hello from ollama\"}"}}`))
	}))
	defer server.Close()

	adapter := NewOllamaAdapter(OllamaConfig{
		Endpoint: server.URL,
		Model:    "llama3.2",
	})
	out, err := Execute(context.Background(), adapter, Request{
		SessionID: "s-ollama-ok",
		BranchID:  "main",
		Packet:    []byte(`{"packet":"tiny"}`),
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out.Response.Decision != "answer" {
		t.Fatalf("decision: got %q", out.Response.Decision)
	}
	if out.Response.Answer != "hello from ollama" {
		t.Fatalf("answer: got %q", out.Response.Answer)
	}
}

func TestMalformedOllamaResponseRejectedCleanly(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":"{\"answer\":\"missing decision\"}"}}`))
	}))
	defer server.Close()

	adapter := NewOllamaAdapter(OllamaConfig{
		Endpoint: server.URL,
		Model:    "llama3.2",
	})
	_, err := Execute(context.Background(), adapter, Request{
		SessionID: "s-ollama-bad",
		BranchID:  "main",
		Packet:    []byte(`{"packet":"tiny"}`),
	})
	if !errors.Is(err, ErrMalformedModelResponse) {
		t.Fatalf("expected malformed response, got %v", err)
	}
}
