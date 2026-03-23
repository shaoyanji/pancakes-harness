package tools

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidRequest  = errors.New("invalid tool request")
	ErrInvalidResponse = errors.New("invalid tool response schema")
)

const (
	ErrorTypeTimeout = "timeout"
	ErrorTypeExec    = "exec"
	ErrorTypeSchema  = "schema"
	ErrorTypeTool    = "tool"
)

type Request struct {
	Tool      string         `json:"tool"`
	CallID    string         `json:"call_id"`
	Args      map[string]any `json:"args"`
	TimeoutMS int            `json:"timeout_ms"`
}

func (r Request) Validate() error {
	if r.Tool == "" || r.CallID == "" {
		return ErrInvalidRequest
	}
	if r.TimeoutMS < 0 {
		return ErrInvalidRequest
	}
	return nil
}

type Artifact struct {
	Name    string `json:"name,omitempty"`
	BlobRef string `json:"blob_ref,omitempty"`
}

type NormalizedError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e NormalizedError) Validate() error {
	switch e.Type {
	case ErrorTypeTimeout, ErrorTypeExec, ErrorTypeSchema, ErrorTypeTool:
	default:
		return ErrInvalidResponse
	}
	if e.Message == "" {
		return ErrInvalidResponse
	}
	return nil
}

type Response struct {
	OK        bool             `json:"ok"`
	CallID    string           `json:"call_id"`
	Result    map[string]any   `json:"result,omitempty"`
	Summary   string           `json:"summary,omitempty"`
	Artifacts []Artifact       `json:"artifacts,omitempty"`
	Error     *NormalizedError `json:"error,omitempty"`
}

func (r Response) ValidateAgainstRequest(req Request) error {
	if req.CallID == "" {
		return ErrInvalidRequest
	}
	if r.CallID != req.CallID {
		return fmt.Errorf("%w: call_id mismatch", ErrInvalidResponse)
	}
	if r.OK {
		if r.Error != nil {
			return fmt.Errorf("%w: success response must not include error", ErrInvalidResponse)
		}
		return nil
	}
	if r.Error == nil {
		return fmt.Errorf("%w: failure response must include error", ErrInvalidResponse)
	}
	return r.Error.Validate()
}
