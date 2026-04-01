package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"pancakes-harness/internal/assembler"
	"pancakes-harness/internal/backend"
	"pancakes-harness/internal/backend/xs"
	"pancakes-harness/internal/metrics"
	"pancakes-harness/internal/model"
	"pancakes-harness/internal/runtime"
	"pancakes-harness/internal/server"
	"pancakes-harness/internal/tools"
)

const (
	envModelMode       = "HARNESS_MODEL_MODE"
	envModelEndpoint   = "HARNESS_MODEL_ENDPOINT"
	envModelAuthHeader = "HARNESS_MODEL_AUTH_HEADER"
	envModelAuthKey    = "HARNESS_MODEL_AUTH_KEY"
	envModelBearer     = "HARNESS_MODEL_BEARER_TOKEN"
	envModelTimeout    = "HARNESS_MODEL_TIMEOUT"
	envOllamaEndpoint  = "HARNESS_OLLAMA_ENDPOINT"
	envOllamaModel     = "HARNESS_OLLAMA_MODEL"
	envBackendMode     = "HARNESS_BACKEND_MODE"
	envSessionID       = "HARNESS_SESSION_ID"
	envBranchID        = "HARNESS_BRANCH_ID"
	envXSCommand       = "HARNESS_XS_COMMAND"
	envServeBind       = "HARNESS_SERVE_BIND"
	envServePort       = "HARNESS_SERVE_PORT"
)

var (
	errEmptyPrompt          = errors.New("prompt is required (provide argv text or stdin)")
	errConflictingAuth      = errors.New("set either auth key/header or bearer token, not both")
	errUnsupportedModelMode = errors.New("unsupported model mode")
	errUnsupportedBackend   = errors.New("unsupported backend mode")
)

const releaseVersion = "0.2.2"

type launcherConfig struct {
	modelMode      string
	modelEndpoint  string
	modelTimeout   time.Duration
	ollamaEndpoint string
	ollamaModel    string
	authHeader     string
	authValue      string

	backendMode string
	xsCommand   string
	sessionID   string
	branchID    string
}

type serveConfig struct {
	launcher launcherConfig
	bindAddr string
	port     int
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	if shouldShowVersion(args) {
		fmt.Fprintf(stdout, "pancakes-harness %s\n", releaseVersion)
		return 0
	}
	if shouldShowHelp(args) {
		if len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "serve") {
			fmt.Fprint(stdout, serveUsage())
		} else {
			fmt.Fprint(stdout, mainUsage())
		}
		return 0
	}
	if len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "serve") {
		return runServe(args[1:], stdout, stderr, getenv)
	}
	return runOneShot(args, stdin, stdout, stderr, getenv)
}

func runOneShot(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
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

	s, err := runtime.StartSession(runtime.Config{
		SessionID:       cfg.sessionID,
		DefaultBranchID: cfg.branchID,
		Backend:         b,
		ModelAdapter:    adapter,
		ToolRunner:      tools.NewRunner(nil),
		ModelHeaders:    buildModelHeaders(cfg),
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

func runServe(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	cfg, err := parseServeConfig(args, getenv)
	if err != nil {
		fmt.Fprintf(stderr, "config error: %v\n", err)
		return 2
	}

	b, err := buildBackend(cfg.launcher)
	if err != nil {
		fmt.Fprintf(stderr, "backend error: %v\n", err)
		return 2
	}
	adapter, err := buildAdapter(cfg.launcher)
	if err != nil {
		fmt.Fprintf(stderr, "adapter error: %v\n", err)
		return 2
	}

	api, err := server.New(server.Config{
		Backend:      b,
		ModelAdapter: adapter,
		ToolRunner:   tools.NewRunner(nil),
		ModelHeaders: buildModelHeaders(cfg.launcher),
		Timeout:      cfg.launcher.modelTimeout,
		Metrics:      metrics.NewRegistry(),
		BackendMode:  cfg.launcher.backendMode,
		ModelMode:    cfg.launcher.modelMode,
	})
	if err != nil {
		fmt.Fprintf(stderr, "server init error: %v\n", err)
		return 1
	}

	addr := fmt.Sprintf("%s:%d", cfg.bindAddr, cfg.port)
	httpSrv := &http.Server{
		Addr:    addr,
		Handler: api.Handler(),
	}
	fmt.Fprintf(stdout, "serving on http://%s\n", addr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(stderr, "serve failed: %v\n", err)
		return 1
	}
	return 0
}

func parseConfig(args []string, stdin io.Reader, getenv func(string) string) (launcherConfig, string, error) {
	cfg, err := parseLauncherFlags(args, getenv)
	if err != nil {
		return launcherConfig{}, "", err
	}
	prompt, err := readPrompt(cfg.remainingArgs, stdin)
	if err != nil {
		return launcherConfig{}, "", err
	}
	return cfg.launcher, prompt, nil
}

func parseServeConfig(args []string, getenv func(string) string) (serveConfig, error) {
	parsed, err := parseLauncherFlags(args, getenv)
	if err != nil {
		return serveConfig{}, err
	}
	if len(parsed.remainingArgs) > 0 {
		return serveConfig{}, errors.New("unexpected positional args for serve mode")
	}
	bind := strings.TrimSpace(parsed.bindAddr)
	if bind == "" {
		return serveConfig{}, errors.New("bind address is required")
	}
	if parsed.port <= 0 {
		return serveConfig{}, errors.New("port must be > 0")
	}

	return serveConfig{
		launcher: parsed.launcher,
		bindAddr: bind,
		port:     parsed.port,
	}, nil
}

type parsedFlags struct {
	launcher      launcherConfig
	bindAddr      string
	port          int
	remainingArgs []string
}

func parseLauncherFlags(args []string, getenv func(string) string) (parsedFlags, error) {
	defaultTimeout, err := parseDurationOrDefault(getenv(envModelTimeout), 10*time.Second)
	if err != nil {
		return parsedFlags{}, err
	}

	modelModeDefault := stringOrDefault(getenv(envModelMode), "mock")
	modelEndpointDefault := strings.TrimSpace(getenv(envModelEndpoint))
	modelAuthHeaderDefault := stringOrDefault(getenv(envModelAuthHeader), "Authorization")
	modelAuthKeyDefault := strings.TrimSpace(getenv(envModelAuthKey))
	modelBearerDefault := strings.TrimSpace(getenv(envModelBearer))
	ollamaEndpointDefault := stringOrDefault(getenv(envOllamaEndpoint), "http://127.0.0.1:11434")
	ollamaModelDefault := strings.TrimSpace(getenv(envOllamaModel))
	backendModeDefault := stringOrDefault(getenv(envBackendMode), "memory")
	sessionIDDefault := stringOrDefault(getenv(envSessionID), "demo")
	branchIDDefault := stringOrDefault(getenv(envBranchID), "main")
	xsCommandDefault := stringOrDefault(getenv(envXSCommand), "xs")
	serveBindDefault := stringOrDefault(getenv(envServeBind), "127.0.0.1")
	servePortDefault, err := parseIntOrDefault(getenv(envServePort), 8080)
	if err != nil {
		return parsedFlags{}, err
	}

	fs := flag.NewFlagSet("harness", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	modelMode := modelModeDefault
	modelEndpoint := modelEndpointDefault
	modelAuthHeader := modelAuthHeaderDefault
	modelAuthKey := modelAuthKeyDefault
	modelBearer := modelBearerDefault
	ollamaEndpoint := ollamaEndpointDefault
	ollamaModel := ollamaModelDefault
	modelTimeout := defaultTimeout
	backendMode := backendModeDefault
	xsCommand := xsCommandDefault
	sessionID := sessionIDDefault
	branchID := branchIDDefault
	serveBind := serveBindDefault
	servePort := servePortDefault

	fs.StringVar(&modelMode, "model-mode", modelModeDefault, "model adapter mode: mock|http|ollama")
	fs.StringVar(&modelEndpoint, "model-endpoint", modelEndpointDefault, "HTTP model endpoint URL")
	fs.StringVar(&modelAuthHeader, "model-auth-header", modelAuthHeaderDefault, "HTTP auth header name")
	fs.StringVar(&modelAuthKey, "model-auth-key", modelAuthKeyDefault, "HTTP auth header value")
	fs.StringVar(&modelBearer, "model-bearer-token", modelBearerDefault, "HTTP bearer token (sent as Bearer token)")
	fs.DurationVar(&modelTimeout, "model-timeout", defaultTimeout, "model call timeout (e.g. 10s)")
	fs.StringVar(&ollamaEndpoint, "ollama-endpoint", ollamaEndpointDefault, "Ollama API base URL")
	fs.StringVar(&ollamaModel, "ollama-model", ollamaModelDefault, "Ollama model name")
	fs.StringVar(&backendMode, "backend-mode", backendModeDefault, "backend mode: memory|xs")
	fs.StringVar(&xsCommand, "xs-command", xsCommandDefault, "xs command path when backend-mode=xs")
	fs.StringVar(&sessionID, "session-id", sessionIDDefault, "session id")
	fs.StringVar(&branchID, "branch-id", branchIDDefault, "branch id")
	fs.StringVar(&serveBind, "bind", serveBindDefault, "bind address (serve mode)")
	fs.IntVar(&servePort, "port", servePortDefault, "bind port (serve mode)")

	if err := fs.Parse(args); err != nil {
		return parsedFlags{}, err
	}

	modelMode = strings.ToLower(strings.TrimSpace(modelMode))
	backendMode = strings.ToLower(strings.TrimSpace(backendMode))
	modelEndpoint = strings.TrimSpace(modelEndpoint)
	modelAuthHeader = strings.TrimSpace(modelAuthHeader)
	modelAuthKey = strings.TrimSpace(modelAuthKey)
	modelBearer = strings.TrimSpace(modelBearer)
	ollamaEndpoint = strings.TrimSpace(ollamaEndpoint)
	ollamaModel = strings.TrimSpace(ollamaModel)
	xsCommand = strings.TrimSpace(xsCommand)
	sessionID = strings.TrimSpace(sessionID)
	branchID = strings.TrimSpace(branchID)

	if modelMode != "mock" && modelMode != "http" && modelMode != "ollama" {
		return parsedFlags{}, fmt.Errorf("%w: %q", errUnsupportedModelMode, modelMode)
	}
	if backendMode != "memory" && backendMode != "xs" {
		return parsedFlags{}, fmt.Errorf("%w: %q", errUnsupportedBackend, backendMode)
	}
	if modelMode == "http" && modelEndpoint == "" {
		return parsedFlags{}, errors.New("model endpoint is required in http mode")
	}
	if modelMode == "ollama" && ollamaModel == "" {
		return parsedFlags{}, errors.New("ollama model is required in ollama mode")
	}
	if modelBearer != "" && modelAuthKey != "" {
		return parsedFlags{}, errConflictingAuth
	}

	authValue := modelAuthKey
	if modelBearer != "" {
		authValue = "Bearer " + modelBearer
	}

	return parsedFlags{
		launcher: launcherConfig{
			modelMode:      modelMode,
			modelEndpoint:  modelEndpoint,
			modelTimeout:   modelTimeout,
			ollamaEndpoint: ollamaEndpoint,
			ollamaModel:    ollamaModel,
			authHeader:     modelAuthHeader,
			authValue:      authValue,
			backendMode:    backendMode,
			xsCommand:      xsCommand,
			sessionID:      sessionID,
			branchID:       branchID,
		},
		bindAddr:      strings.TrimSpace(serveBind),
		port:          servePort,
		remainingArgs: fs.Args(),
	}, nil
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
	case "ollama":
		return model.NewOllamaAdapter(model.OllamaConfig{
			Endpoint: cfg.ollamaEndpoint,
			Model:    cfg.ollamaModel,
			Timeout:  cfg.modelTimeout,
		}), nil
	default:
		return nil, fmt.Errorf("%w: %q", errUnsupportedModelMode, cfg.modelMode)
	}
}

func buildModelHeaders(cfg launcherConfig) []assembler.Header {
	headers := []assembler.Header{
		{Name: "Content-Type", Value: "application/json"},
	}
	if cfg.authValue != "" {
		headers = append(headers, assembler.Header{Name: cfg.authHeader, Value: cfg.authValue})
	}
	return headers
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

func parseIntOrDefault(raw string, d int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return d, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid %s=%q", envServePort, raw)
	}
	return n, nil
}

func shouldShowHelp(args []string) bool {
	for _, arg := range args {
		switch strings.TrimSpace(arg) {
		case "-h", "--help", "help":
			return true
		}
	}
	return false
}

func shouldShowVersion(args []string) bool {
	for _, arg := range args {
		switch strings.TrimSpace(arg) {
		case "-version", "--version", "version":
			return true
		}
	}
	return false
}

func mainUsage() string {
	return `pancakes-harness 0.2.2

Local-first context and egress kernel.
It reconstructs local consult context, shapes a bounded model-facing artifact, preserves replayable branch state, and exposes a thin ingress API.
It is intentionally not the full agent execution/control plane.

Usage:
  harness [flags] <prompt>
  harness [flags] < <prompt-file>
  harness serve [flags]

One-shot flags:
  -model-mode string
        model adapter mode: mock|http|ollama
  -model-endpoint string
        HTTP model endpoint URL
  -model-auth-header string
        HTTP auth header name
  -model-auth-key string
        HTTP auth header value
  -model-bearer-token string
        HTTP bearer token (sent as Bearer token)
  -model-timeout duration
        model call timeout (e.g. 10s)
  -ollama-endpoint string
        Ollama API base URL
  -ollama-model string
        Ollama model name
  -backend-mode string
        backend mode: memory|xs
  -xs-command string
        xs command path when backend-mode=xs
  -session-id string
        session id
  -branch-id string
        branch id

Serve flags:
  -bind string
        bind address (serve mode)
  -port int
        bind port (serve mode)

Examples:
  harness -model-mode mock "hello harness"
  harness serve -model-mode ollama -ollama-model qwen3:0.6b
  harness -version
`
}

func serveUsage() string {
	return `pancakes-harness 0.2.2

Usage:
  harness serve [flags]

Flags:
  -model-mode string
        model adapter mode: mock|http|ollama
  -model-endpoint string
        HTTP model endpoint URL
  -model-auth-header string
        HTTP auth header name
  -model-auth-key string
        HTTP auth header value
  -model-bearer-token string
        HTTP bearer token (sent as Bearer token)
  -model-timeout duration
        model call timeout (e.g. 10s)
  -ollama-endpoint string
        Ollama API base URL
  -ollama-model string
        Ollama model name
  -backend-mode string
        backend mode: memory|xs
  -xs-command string
        xs command path when backend-mode=xs
  -session-id string
        default session id for one-shot parity
  -branch-id string
        default branch id for one-shot parity
  -bind string
        bind address (serve mode)
  -port int
        bind port (serve mode)

Example:
  harness serve -model-mode ollama -ollama-model qwen3:0.6b -bind 127.0.0.1 -port 8080
`
}
