package preflight

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestValidateResolved(t *testing.T) {
	t.Parallel()

	res, err := Validate(Input{
		Mode:           "agent_call",
		Scope:          "branch:main",
		AllowExecution: true,
		AllowTools:     true,
		Refs:           []string{"ref-b", "ref-a"},
		Constraints: map[string]string{
			"tier":  "fast",
			"model": "xs",
		},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !res.Resolved {
		t.Fatalf("expected resolved result, got %#v", res)
	}
	if len(res.Missing) != 0 {
		t.Fatalf("expected no missing fields, got %#v", res.Missing)
	}
	if !reflect.DeepEqual(res.Refs, []string{"ref-a", "ref-b"}) {
		t.Fatalf("expected sorted refs, got %#v", res.Refs)
	}
}

func TestValidateUnresolvedWithMissingFields(t *testing.T) {
	t.Parallel()

	res, err := Validate(Input{
		Mode:           "agent_call",
		AllowExecution: true,
		AllowTools:     false,
		Refs:           []string{"ref-z"},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if res.Resolved {
		t.Fatalf("expected unresolved result, got %#v", res)
	}
	if !reflect.DeepEqual(res.Missing, []string{"scope"}) {
		t.Fatalf("expected missing scope, got %#v", res.Missing)
	}
}

func TestValidateMalformedInput(t *testing.T) {
	t.Parallel()

	res, err := Validate(Input{
		Mode:        "   ",
		Scope:       "branch:main",
		AllowTools:  true,
		Constraints: map[string]string{"k": "v"},
	})
	if !errors.Is(err, ErrMalformedInput) {
		t.Fatalf("expected malformed input error, got %v", err)
	}
	if res.Resolved {
		t.Fatalf("malformed input cannot be resolved: %#v", res)
	}
	if !reflect.DeepEqual(res.Missing, []string{"mode"}) {
		t.Fatalf("expected missing mode, got %#v", res.Missing)
	}
}

func TestValidateDeterministicStableOutput(t *testing.T) {
	t.Parallel()

	in := Input{
		Mode:           "agent_call",
		Scope:          "branch:main",
		AllowExecution: false,
		AllowTools:     true,
		Refs:           []string{"ref-2", "ref-1"},
		Constraints: map[string]string{
			"b": "2",
			"a": "1",
		},
	}

	res1, err := Validate(in)
	if err != nil {
		t.Fatalf("validate 1: %v", err)
	}
	res2, err := Validate(in)
	if err != nil {
		t.Fatalf("validate 2: %v", err)
	}

	b1, err := json.Marshal(res1)
	if err != nil {
		t.Fatalf("marshal 1: %v", err)
	}
	b2, err := json.Marshal(res2)
	if err != nil {
		t.Fatalf("marshal 2: %v", err)
	}
	if string(b1) != string(b2) {
		t.Fatalf("expected stable output, got %s vs %s", string(b1), string(b2))
	}
}
