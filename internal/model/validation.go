package model

import (
	"encoding/json"
	"fmt"
)

func ParseAndValidateResponse(raw []byte) (Response, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return Response{}, fmt.Errorf("%w: invalid json", ErrMalformedModelResponse)
	}
	if _, ok := top["decision"]; !ok {
		return Response{}, fmt.Errorf("%w: missing decision", ErrMalformedModelResponse)
	}

	var resp Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return Response{}, fmt.Errorf("%w: schema decode failed", ErrMalformedModelResponse)
	}
	if err := resp.Validate(); err != nil {
		return Response{}, err
	}
	return resp, nil
}
