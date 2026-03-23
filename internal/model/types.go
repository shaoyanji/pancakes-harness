package model

import (
	"encoding/json"
	"errors"
)

var (
	ErrInvalidRequest         = errors.New("invalid model request")
	ErrMalformedModelResponse = errors.New("malformed model response")
	ErrAdapterCallFailed      = errors.New("model adapter call failed")
	ErrBackendPersistFailed   = errors.New("backend persist failed")
)

type Request struct {
	SessionID string
	BranchID  string
	Packet    []byte
}

func (r Request) Validate() error {
	if r.SessionID == "" || r.BranchID == "" {
		return ErrInvalidRequest
	}
	if len(r.Packet) == 0 || !json.Valid(r.Packet) {
		return ErrInvalidRequest
	}
	return nil
}

type ToolCall struct {
	Tool   string         `json:"tool"`
	CallID string         `json:"call_id,omitempty"`
	Args   map[string]any `json:"args,omitempty"`
}

type Response struct {
	Decision           string         `json:"decision"`
	Answer             string         `json:"answer,omitempty"`
	ToolCalls          []ToolCall     `json:"tool_calls,omitempty"`
	SummaryDelta       string         `json:"summary_delta,omitempty"`
	BranchOps          []string       `json:"branch_ops,omitempty"`
	UnresolvedRefs     []string       `json:"unresolved_refs,omitempty"`
	RawProviderPayload map[string]any `json:"raw_provider_payload,omitempty"`
}

func (r Response) Validate() error {
	if r.Decision == "" {
		return ErrMalformedModelResponse
	}
	switch r.Decision {
	case "answer", "tool_calls", "continue":
	default:
		return ErrMalformedModelResponse
	}
	if r.Decision == "answer" && r.Answer == "" {
		return ErrMalformedModelResponse
	}
	if r.Decision == "tool_calls" && len(r.ToolCalls) == 0 {
		return ErrMalformedModelResponse
	}
	for _, tc := range r.ToolCalls {
		if tc.Tool == "" {
			return ErrMalformedModelResponse
		}
	}
	return nil
}

type CallResult struct {
	Response Response
	Raw      []byte
}
