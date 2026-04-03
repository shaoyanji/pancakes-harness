package model

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type ollamaRoundTripFunc func(*http.Request) (*http.Response, error)

func (f ollamaRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func newOllamaTestClient(handler http.Handler) *http.Client {
	return &http.Client{
		Transport: ollamaRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)
			return rec.Result(), nil
		}),
	}
}

func TestOllamaStructuredResponsePassesValidation(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":"{\"decision\":\"answer\",\"answer\":\"hello from ollama\"}"}}`))
	})

	adapter := NewOllamaAdapter(OllamaConfig{
		Endpoint: "http://local.test",
		Model:    "llama3.2",
	})
	adapter.client = newOllamaTestClient(handler)
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

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":{"content":"{\"answer\":\"missing decision\"}"}}`))
	})

	adapter := NewOllamaAdapter(OllamaConfig{
		Endpoint: "http://local.test",
		Model:    "llama3.2",
	})
	adapter.client = newOllamaTestClient(handler)
	_, err := Execute(context.Background(), adapter, Request{
		SessionID: "s-ollama-bad",
		BranchID:  "main",
		Packet:    []byte(`{"packet":"tiny"}`),
	})
	if !errors.Is(err, ErrMalformedModelResponse) {
		t.Fatalf("expected malformed response, got %v", err)
	}
}
