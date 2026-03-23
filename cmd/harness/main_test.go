package main

import (
	"strings"
	"testing"
	"time"

	"pancakes-harness/internal/model"
)

func TestReadPromptFromArgs(t *testing.T) {
	t.Parallel()

	prompt, err := readPrompt([]string{"hello", "launcher"}, strings.NewReader(""))
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	if prompt != "hello launcher" {
		t.Fatalf("unexpected prompt %q", prompt)
	}
}

func TestReadPromptFromStdin(t *testing.T) {
	t.Parallel()

	prompt, err := readPrompt(nil, strings.NewReader("  from stdin  \n"))
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	if prompt != "from stdin" {
		t.Fatalf("unexpected prompt %q", prompt)
	}
}

func TestParseConfigHTTPModeRequiresEndpoint(t *testing.T) {
	t.Parallel()

	_, _, err := parseConfig([]string{"-model-mode", "http", "hi"}, strings.NewReader(""), func(string) string {
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "model endpoint is required") {
		t.Fatalf("expected missing endpoint error, got %v", err)
	}
}

func TestParseConfigConflictingAuthRejected(t *testing.T) {
	t.Parallel()

	_, _, err := parseConfig(
		[]string{"-model-mode", "http", "-model-endpoint", "http://localhost:8080", "-model-auth-key", "k", "-model-bearer-token", "t", "hi"},
		strings.NewReader(""),
		func(string) string { return "" },
	)
	if err == nil || !strings.Contains(err.Error(), errConflictingAuth.Error()) {
		t.Fatalf("expected conflicting auth error, got %v", err)
	}
}

func TestParseConfigUsesEnvDefaults(t *testing.T) {
	t.Parallel()

	getenv := func(k string) string {
		switch k {
		case envModelMode:
			return "http"
		case envModelEndpoint:
			return "http://127.0.0.1:1234/v1/chat"
		case envModelBearer:
			return "tok"
		case envModelTimeout:
			return "3s"
		case envBackendMode:
			return "memory"
		case envSessionID:
			return "sess-env"
		case envBranchID:
			return "branch-env"
		default:
			return ""
		}
	}

	cfg, prompt, err := parseConfig([]string{"hello"}, strings.NewReader(""), getenv)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg.modelMode != "http" {
		t.Fatalf("model mode: got %q", cfg.modelMode)
	}
	if cfg.modelEndpoint != "http://127.0.0.1:1234/v1/chat" {
		t.Fatalf("endpoint: got %q", cfg.modelEndpoint)
	}
	if cfg.authValue != "Bearer tok" {
		t.Fatalf("auth value: got %q", cfg.authValue)
	}
	if cfg.modelTimeout != 3*time.Second {
		t.Fatalf("timeout: got %s", cfg.modelTimeout)
	}
	if cfg.sessionID != "sess-env" || cfg.branchID != "branch-env" {
		t.Fatalf("session/branch mismatch: %q %q", cfg.sessionID, cfg.branchID)
	}
	if prompt != "hello" {
		t.Fatalf("prompt: got %q", prompt)
	}
}

func TestLauncherCanSelectOllamaMode(t *testing.T) {
	t.Parallel()

	getenv := func(k string) string {
		switch k {
		case envModelMode:
			return "ollama"
		case envOllamaEndpoint:
			return "http://127.0.0.1:11434"
		case envOllamaModel:
			return "llama3.2"
		default:
			return ""
		}
	}

	cfg, _, err := parseConfig([]string{"hello"}, strings.NewReader(""), getenv)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg.modelMode != "ollama" {
		t.Fatalf("model mode: got %q", cfg.modelMode)
	}
	a, err := buildAdapter(cfg)
	if err != nil {
		t.Fatalf("build adapter: %v", err)
	}
	if _, ok := a.(*model.OllamaAdapter); !ok {
		t.Fatalf("expected ollama adapter, got %T", a)
	}
}
