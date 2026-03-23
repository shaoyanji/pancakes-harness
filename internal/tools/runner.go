package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

type CommandSpec struct {
	Path string
	Args []string
}

type Runner struct {
	commands map[string]CommandSpec
}

func NewRunner(commands map[string]CommandSpec) *Runner {
	copied := make(map[string]CommandSpec, len(commands))
	for k, v := range commands {
		copied[k] = v
	}
	return &Runner{commands: copied}
}

func (r *Runner) Run(ctx context.Context, req Request) Response {
	if err := req.Validate(); err != nil {
		return FailureFromError(req.CallID, ErrorTypeSchema, err.Error())
	}

	spec, ok := r.commands[req.Tool]
	if !ok || spec.Path == "" {
		return FailureFromError(req.CallID, ErrorTypeExec, "tool executable not configured")
	}

	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	input, _ := json.Marshal(req)
	cmd := exec.CommandContext(runCtx, spec.Path, spec.Args...)
	cmd.Stdin = bytes.NewReader(input)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if runCtx.Err() != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return FailureFromError(req.CallID, ErrorTypeTimeout, "tool execution timed out")
	}
	if err != nil {
		msg := "tool subprocess failed"
		if stderr.Len() > 0 {
			msg = fmt.Sprintf("%s: %s", msg, stderr.String())
		}
		return FailureFromError(req.CallID, ErrorTypeExec, msg)
	}

	resp, decodeErr := DecodeAndNormalizeResponse(stdout.Bytes(), req)
	if decodeErr != nil {
		return FailureFromError(req.CallID, ErrorTypeSchema, decodeErr.Error())
	}
	return resp
}

func FailureFromError(callID, typ, message string) Response {
	if callID == "" {
		callID = "unknown"
	}
	switch typ {
	case ErrorTypeTimeout, ErrorTypeExec, ErrorTypeSchema, ErrorTypeTool:
	default:
		typ = ErrorTypeTool
	}
	if message == "" {
		message = "tool execution failed"
	}
	return Response{
		OK:     false,
		CallID: callID,
		Error: &NormalizedError{
			Type:    typ,
			Message: message,
		},
	}
}

func DecodeAndNormalizeResponse(raw []byte, req Request) (Response, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return Response{}, fmt.Errorf("%w: invalid json: %v", ErrInvalidResponse, err)
	}

	var resp Response
	if v, ok := top["ok"]; ok {
		if err := json.Unmarshal(v, &resp.OK); err != nil {
			return Response{}, fmt.Errorf("%w: ok must be boolean", ErrInvalidResponse)
		}
	} else {
		return Response{}, fmt.Errorf("%w: missing ok", ErrInvalidResponse)
	}

	if v, ok := top["call_id"]; ok {
		if err := json.Unmarshal(v, &resp.CallID); err != nil || resp.CallID == "" {
			return Response{}, fmt.Errorf("%w: invalid call_id", ErrInvalidResponse)
		}
	} else {
		return Response{}, fmt.Errorf("%w: missing call_id", ErrInvalidResponse)
	}

	if resp.OK {
		if _, ok := top["result"]; !ok {
			return Response{}, fmt.Errorf("%w: success response missing result", ErrInvalidResponse)
		}
		if _, ok := top["summary"]; !ok {
			return Response{}, fmt.Errorf("%w: success response missing summary", ErrInvalidResponse)
		}
		if _, ok := top["artifacts"]; !ok {
			return Response{}, fmt.Errorf("%w: success response missing artifacts", ErrInvalidResponse)
		}
		if err := json.Unmarshal(top["result"], &resp.Result); err != nil {
			return Response{}, fmt.Errorf("%w: invalid result", ErrInvalidResponse)
		}
		if err := json.Unmarshal(top["summary"], &resp.Summary); err != nil {
			return Response{}, fmt.Errorf("%w: invalid summary", ErrInvalidResponse)
		}
		if err := json.Unmarshal(top["artifacts"], &resp.Artifacts); err != nil {
			return Response{}, fmt.Errorf("%w: invalid artifacts", ErrInvalidResponse)
		}
	} else {
		errField, ok := top["error"]
		if !ok {
			return Response{}, fmt.Errorf("%w: failure response missing error", ErrInvalidResponse)
		}
		var te NormalizedError
		if err := json.Unmarshal(errField, &te); err != nil {
			return Response{}, fmt.Errorf("%w: invalid error field", ErrInvalidResponse)
		}
		if err := te.Validate(); err != nil {
			te.Type = ErrorTypeTool
			if te.Message == "" {
				te.Message = "tool reported failure"
			}
		}
		resp.Error = &te
	}

	if err := resp.ValidateAgainstRequest(req); err != nil {
		return Response{}, err
	}
	return resp, nil
}
