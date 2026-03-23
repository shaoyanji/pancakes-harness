package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"pancakes-harness/internal/assembler"
	"pancakes-harness/internal/backend"
	"pancakes-harness/internal/backend/xs"
	"pancakes-harness/internal/model"
	"pancakes-harness/internal/runtime"
	"pancakes-harness/internal/tools"
)

const (
	envModelMode       = "HARNESS_MODEL_MODE"
	envModelEndpoint   = "HARNESS_MODEL_ENDPOINT"
	envModelAuthHeader = "HARNESS_MODEL_AUTH_HEADER"
	envModelAuthKey    = "HARNESS_MODEL_AUTH_KEY"
	envModelBearer     = "HARNESS_MODEL_BEARER_TOKEN"
	envModelTimeout    = "HARNESS_MODEL_TIMEOUT"
	envBackendMode     = "HARNESS_BACKEND_MODE"
	envSessionID       = "HARNESS_SESSION_ID"
	envBranchID        = "HARNESS_BRANCH_ID"
	envXSCommand       = "HARNESS_XS_COMMAND"
)

var (
	errEmptyPrompt          = errors.New("prompt is required (provide argv text or stdin)")
	errConflictingAuth      = errors.New("set either auth key/header or bearer token, not both")
	errUnsupportedModelMode = errors.New("unsupported model mode")
	errUnsupportedBackend   = errors.New("unsupported backend mode")
)

type launcherConfig struct {
	modelMode     string
	modelEndpoint string
	modelTimeout  time.Duration
	authHeader    string
	authValue     string

	backendMode string
	xsCommand   string
	sessionID   string
	branchID    string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	cfg, prompt, err := parseConfig(args, stdin, getenv)
	if err != nil {
		fmt.Fprintf(stderr, "config error: %v\n", err)
		return 2
	}

	b, err := buildBackend(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "backend error: %v\n", err)
		return 2
	}

	adapter, err := buildAdapter(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "adapter error: %v\n", err)
		return 2
	}

	headers := []assembler.Header{
		{Name: "Content-Type", Value: "application/json"},
	}
	if cfg.authValue != "" {
		headers = append(headers, assembler.Header{Name: cfg.authHeader, Value: cfg.authValue})
	}

	s, err := runtime.StartSession(runtime.Config{
		SessionID:       cfg.sessionID,
		DefaultBranchID: cfg.branchID,
		Backend:         b,
		ModelAdapter:    adapter,
		ToolRunner:      tools.NewRunner(nil),
		ModelHeaders:    headers,
	})
	if err != nil {
		fmt.Fprintf(stderr, "start session: %v\n", err)
		return 1
	}

	ctx := context.Background()
	var cancel context.CancelFunc
	if cfg.modelTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, cfg.modelTimeout)
		defer cancel()
	}

	out, err := s.HandleUserTurn(ctx, cfg.branchID, prompt)
	if err != nil {
		fmt.Fprintf(stderr, "turn failed: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "answer: %s\n", out.Answer)
	fmt.Fprintf(stdout, "envelope_bytes: %d\n", out.PacketEnvelopeBytes)
	fmt.Fprintf(stdout, "session: %s\n", out.SessionID)
	fmt.Fprintf(stdout, "branch: %s\n", out.BranchID)
	return 0
}

func parseConfig(args []string, stdin io.Reader, getenv func(string) string) (launcherConfig, string, error) {
	defaultTimeout, err := parseDurationOrDefault(getenv(envModelTimeout), 10*time.Second)
	if err != nil {
		return launcherConfig{}, "", err
	}

	modelModeDefault := stringOrDefault(getenv(envModelMode), "mock")
	modelEndpointDefault := strings.TrimSpace(getenv(envModelEndpoint))
	modelAuthHeaderDefault := stringOrDefault(getenv(envModelAuthHeader), "Authorization")
	modelAuthKeyDefault := strings.TrimSpace(getenv(envModelAuthKey))
	modelBearerDefault := strings.TrimSpace(getenv(envModelBearer))
	backendModeDefault := stringOrDefault(getenv(envBackendMode), "memory")
	sessionIDDefault := stringOrDefault(getenv(envSessionID), "demo")
	branchIDDefault := stringOrDefault(getenv(envBranchID), "main")
	xsCommandDefault := stringOrDefault(getenv(envXSCommand), "xs")

	fs := flag.NewFlagSet("harness", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	modelMode := modelModeDefault
	modelEndpoint := modelEndpointDefault
	modelAuthHeader := modelAuthHeaderDefault
	modelAuthKey := modelAuthKeyDefault
	modelBearer := modelBearerDefault
	modelTimeout := defaultTimeout
	backendMode := backendModeDefault
	xsCommand := xsCommandDefault
	sessionID := sessionIDDefault
	branchID := branchIDDefault

	fs.StringVar(&modelMode, "model-mode", modelModeDefault, "model adapter mode: mock|http")
	fs.StringVar(&modelEndpoint, "model-endpoint", modelEndpointDefault, "HTTP model endpoint URL")
	fs.StringVar(&modelAuthHeader, "model-auth-header", modelAuthHeaderDefault, "HTTP auth header name")
	fs.StringVar(&modelAuthKey, "model-auth-key", modelAuthKeyDefault, "HTTP auth header value")
	fs.StringVar(&modelBearer, "model-bearer-token", modelBearerDefault, "HTTP bearer token (sent as Bearer token)")
	fs.DurationVar(&modelTimeout, "model-timeout", defaultTimeout, "model call timeout (e.g. 10s)")
	fs.StringVar(&backendMode, "backend-mode", backendModeDefault, "backend mode: memory|xs")
	fs.StringVar(&xsCommand, "xs-command", xsCommandDefault, "xs command path when backend-mode=xs")
	fs.StringVar(&sessionID, "session-id", sessionIDDefault, "session id")
	fs.StringVar(&branchID, "branch-id", branchIDDefault, "branch id")

	if err := fs.Parse(args); err != nil {
		return launcherConfig{}, "", err
	}

	prompt, err := readPrompt(fs.Args(), stdin)
	if err != nil {
		return launcherConfig{}, "", err
	}

	modelMode = strings.ToLower(strings.TrimSpace(modelMode))
	backendMode = strings.ToLower(strings.TrimSpace(backendMode))
	modelEndpoint = strings.TrimSpace(modelEndpoint)
	modelAuthHeader = strings.TrimSpace(modelAuthHeader)
	modelAuthKey = strings.TrimSpace(modelAuthKey)
	modelBearer = strings.TrimSpace(modelBearer)
	xsCommand = strings.TrimSpace(xsCommand)
	sessionID = strings.TrimSpace(sessionID)
	branchID = strings.TrimSpace(branchID)

	if modelMode != "mock" && modelMode != "http" {
		return launcherConfig{}, "", fmt.Errorf("%w: %q", errUnsupportedModelMode, modelMode)
	}
	if backendMode != "memory" && backendMode != "xs" {
		return launcherConfig{}, "", fmt.Errorf("%w: %q", errUnsupportedBackend, backendMode)
	}
	if modelMode == "http" && modelEndpoint == "" {
		return launcherConfig{}, "", errors.New("model endpoint is required in http mode")
	}
	if modelBearer != "" && modelAuthKey != "" {
		return launcherConfig{}, "", errConflictingAuth
	}

	authValue := modelAuthKey
	if modelBearer != "" {
		authValue = "Bearer " + modelBearer
	}

	cfg := launcherConfig{
		modelMode:     modelMode,
		modelEndpoint: modelEndpoint,
		modelTimeout:  modelTimeout,
		authHeader:    modelAuthHeader,
		authValue:     authValue,
		backendMode:   backendMode,
		xsCommand:     xsCommand,
		sessionID:     sessionID,
		branchID:      branchID,
	}
	return cfg, prompt, nil
}

func readPrompt(args []string, stdin io.Reader) (string, error) {
	if len(args) > 0 {
		prompt := strings.TrimSpace(strings.Join(args, " "))
		if prompt != "" {
			return prompt, nil
		}
	}
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return "", err
	}
	prompt := strings.TrimSpace(string(raw))
	if prompt == "" {
		return "", errEmptyPrompt
	}
	return prompt, nil
}

func buildBackend(cfg launcherConfig) (backend.Backend, error) {
	switch cfg.backendMode {
	case "memory":
		return backend.NewMemoryBackend(), nil
	case "xs":
		return xs.NewAdapter(xs.Config{Command: cfg.xsCommand}), nil
	default:
		return nil, fmt.Errorf("%w: %q", errUnsupportedBackend, cfg.backendMode)
	}
}

func buildAdapter(cfg launcherConfig) (model.Adapter, error) {
	switch cfg.modelMode {
	case "mock":
		return model.MockAdapter{
			NameValue: "demo-mock",
			CallFunc: func(ctx context.Context, req model.Request) ([]byte, error) {
				return []byte(`{"decision":"answer","answer":"demo response"}`), nil
			},
		}, nil
	case "http":
		return model.NewHTTPAdapter(model.HTTPConfig{
			Endpoint:     cfg.modelEndpoint,
			APIKey:       cfg.authValue,
			APIKeyHeader: cfg.authHeader,
			Timeout:      cfg.modelTimeout,
		}), nil
	default:
		return nil, fmt.Errorf("%w: %q", errUnsupportedModelMode, cfg.modelMode)
	}
}

func stringOrDefault(v, d string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return d
	}
	return v
}

func parseDurationOrDefault(raw string, d time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return d, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s=%q: %w", envModelTimeout, raw, err)
	}
	return parsed, nil
}
