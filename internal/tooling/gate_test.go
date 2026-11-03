package tooling

import (
	"os"
	"path/filepath"
	"testing"
)

type mapIndex struct {
	files map[string]bool
	dirs  map[string]bool
}

func (m mapIndex) HasPath(rel string) bool { return m.files[rel] }
func (m mapIndex) HasDir(rel string) bool  { return m.dirs[rel] }

func testGate() *Gate {
	return NewGate(builtinRegistry(), mapIndex{
		files: map[string]bool{
			"internal/server/server.go":     true,
			"internal/preprocess/needle.go": true,
			"run-demo.sh":                   true,
		},
		dirs: map[string]bool{"": true, "internal": true, "internal/server": true,
			"internal/preprocess": true},
	})
}

func builtinRegistry() *Registry {
	reg := NewRegistry()
	RegisterBuiltinTools(reg)
	return reg
}

func TestGateRules(t *testing.T) {
	query := `find vendorHash in the text "the vendor hash pins nixpkgs"`

	cases := []struct {
		name   string
		query  string
		call   Call
		want   Verdict
		reason string
	}{
		{
			name:  "grounded grep is accepted",
			query: query,
			call: Call{Name: "grep", Args: map[string]any{
				"pattern": "vendorHash", "text": "the vendor hash pins nixpkgs"}},
			want: Accept,
		},
		{
			name:  "unknown tool abstains",
			query: "do something",
			call:  Call{Name: "edit", Args: map[string]any{"ref": "a.go"}},
			want:  Abstain, reason: "tool_exists",
		},
		{
			name:  "invented key abstains",
			query: "open internal/server/server.go",
			call:  Call{Name: "read", Args: map[string]any{"file_name": "internal/server/server.go"}},
			want:  Abstain, reason: "schema",
		},
		{
			name:  "missing argument abstains",
			query: query,
			call:  Call{Name: "grep", Args: map[string]any{"pattern": "vendorHash"}},
			want:  Abstain, reason: "schema",
		},
		{
			name:  "value absent from the request abstains",
			query: "open internal/server/server.go",
			call:  Call{Name: "read", Args: map[string]any{"ref": "internal/preprocess/needle.go"}},
			want:  Abstain, reason: "grounding",
		},
		{
			name:  "read of a path that does not exist abstains",
			query: "open internal/server/missing.go",
			call:  Call{Name: "read", Args: map[string]any{"ref": "internal/server/missing.go"}},
			want:  Abstain, reason: "resolution",
		},
		{
			name:  "read of an existing path is accepted",
			query: "open internal/server/server.go",
			call:  Call{Name: "read", Args: map[string]any{"ref": "internal/server/server.go"}},
			want:  Accept,
		},
		{
			name:  "write may create a file in an existing directory",
			query: `write "add retry budget" to internal/server/retry.go`,
			call: Call{Name: "write", Args: map[string]any{
				"ref": "internal/server/retry.go", "content": "add retry budget"}},
			want: Accept,
		},
		{
			name:  "write into a missing directory abstains",
			query: `write "add retry budget" to internal/nope/retry.go`,
			call: Call{Name: "write", Args: map[string]any{
				"ref": "internal/nope/retry.go", "content": "add retry budget"}},
			want: Abstain, reason: "resolution",
		},
		{
			name:  "glob over an existing directory is accepted",
			query: "list internal/server/*.go",
			call:  Call{Name: "glob", Args: map[string]any{"pattern": "internal/server/*.go"}},
			want:  Accept,
		},
		{
			name:  "glob over a missing directory abstains",
			query: "list internal/nope/*.go",
			call:  Call{Name: "glob", Args: map[string]any{"pattern": "internal/nope/*.go"}},
			want:  Abstain, reason: "resolution",
		},
	}

	gate := testGate()
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			decision := gate.Check(test.query, test.call)
			if decision.Verdict != test.want {
				t.Fatalf("verdict = %s, want %s (%s)", decision.Verdict, test.want, decision.Reason)
			}
			if test.reason != "" && decision.Checks[len(decision.Checks)-1].Rule != test.reason {
				t.Fatalf("failed rule = %s, want %s", decision.Checks[len(decision.Checks)-1].Rule, test.reason)
			}
		})
	}
}

func TestFits(t *testing.T) {
	cases := []struct {
		query string
		want  bool
	}{
		{"open internal/server/server.go", true},
		{"cat the contents of run-demo.sh", true},
		{"give me every file matching internal/preprocess/*.go", true},
		{`search "the vendor hash is pinned" for vendorHash`, true},
		{"what should I have for dinner tonight?", false},
		{"summarise the history of the Byzantine empire.", false},
		{"tell me a joke about compilers.", false},
		{"open internal/nope/missing.go", false},
	}

	gate := testGate()
	for _, test := range cases {
		if got := gate.Fits(test.query); got != test.want {
			t.Errorf("Fits(%q) = %v, want %v", test.query, got, test.want)
		}
	}
}

func TestCheckAllAbstainsOutsideTheToolSet(t *testing.T) {
	gate := testGate()
	kept, decisions := gate.CheckAll("what should I have for dinner tonight?", []Call{
		{Name: "grep", Args: map[string]any{"pattern": "dinner", "text": "dinner tonight"}},
	})

	if len(kept) != 0 {
		t.Fatalf("kept = %+v, want nothing", kept)
	}
	if len(decisions) != 1 || decisions[0].Checks[0].Rule != "request_supported" {
		t.Fatalf("decisions = %+v, want one request_supported refusal", decisions)
	}
}

func TestCheckAllKeepsTheSurvivingCall(t *testing.T) {
	gate := testGate()
	query := `read internal/server/server.go and then list internal/server/*.go`
	kept, decisions := gate.CheckAll(query, []Call{
		{Name: "read", Args: map[string]any{"ref": "internal/server/server.go"}},
		{Name: "read", Args: map[string]any{"ref": "internal/server/gone.go"}},
	})

	if len(decisions) != 2 {
		t.Fatalf("decisions = %d, want 2", len(decisions))
	}
	if len(kept) != 1 || kept[0].Args["ref"] != "internal/server/server.go" {
		t.Fatalf("kept = %+v, want only the resolvable read", kept)
	}
}

func TestIndexFS(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.go"), "package a")
	mustWrite(t, filepath.Join(root, "internal", "b.go"), "package b")
	mustWrite(t, filepath.Join(root, ".git", "HEAD"), "ref: x")

	index, err := IndexFS(root)
	if err != nil {
		t.Fatalf("IndexFS: %v", err)
	}

	for _, path := range []string{"a.go", "internal/b.go"} {
		if !index.HasPath(path) {
			t.Fatalf("HasPath(%q) = false", path)
		}
	}
	if index.HasPath(".git/HEAD") {
		t.Fatal("skipped tree was indexed")
	}
	for _, dir := range []string{"", "internal"} {
		if !index.HasDir(dir) {
			t.Fatalf("HasDir(%q) = false", dir)
		}
	}
	if index.HasDir("nope") {
		t.Fatal("HasDir(nope) = true")
	}
	if index.Len() != 2 {
		t.Fatalf("Len = %d, want 2", index.Len())
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
