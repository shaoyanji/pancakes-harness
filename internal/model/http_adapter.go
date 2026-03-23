package model

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type HTTPConfig struct {
	Endpoint     string
	APIKey       string
	APIKeyHeader string
	Timeout      time.Duration
	ExtraHeaders map[string]string
}

type HTTPAdapter struct {
	cfg    HTTPConfig
	client *http.Client
}

func NewHTTPAdapter(cfg HTTPConfig) *HTTPAdapter {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	header := strings.TrimSpace(cfg.APIKeyHeader)
	if header == "" {
		cfg.APIKeyHeader = "Authorization"
	}
	return &HTTPAdapter{
		cfg:    cfg,
		client: &http.Client{Timeout: timeout},
	}
}

func (a *HTTPAdapter) Name() string { return "http" }

func (a *HTTPAdapter) StatelessCall(ctx context.Context, req Request) ([]byte, error) {
	if strings.TrimSpace(a.cfg.Endpoint) == "" {
		return nil, fmt.Errorf("%w: endpoint is required", ErrAdapterCallFailed)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.Endpoint, bytes.NewReader(req.Packet))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAdapterCallFailed, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range a.cfg.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}
	if a.cfg.APIKey != "" {
		httpReq.Header.Set(a.cfg.APIKeyHeader, a.cfg.APIKey)
	}

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
	return payload, nil
}
