package tooling

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Verdict is the gate's decision on one proposed call.
type Verdict string

const (
	// Accept means the call is structurally valid and every value it carries
	// names something the harness can actually reach.
	Accept Verdict = "accept"
	// Abstain means no tool in the registry can serve this call as emitted.
	Abstain Verdict = "abstain"
)

// Call is one proposed invocation as a model emitted it.
type Call struct {
	Name string         `json:"name"`
	Args map[string]any `json:"arguments"`
}

// Check records the outcome of a single rule, so a verdict can be audited
// instead of trusted.
type Check struct {
	Rule   string `json:"rule"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// Decision is the verdict plus the trail that produced it.
type Decision struct {
	Verdict Verdict `json:"verdict"`
	Reason  string  `json:"reason"`
	Call    Call    `json:"call"`
	Checks  []Check `json:"checks"`
}

// PathIndex answers whether a reference names something the harness can open.
// A filesystem or the store both satisfy it; the gate only needs the question.
type PathIndex interface {
	HasPath(rel string) bool
	HasDir(rel string) bool
}

// pathArg says which argument of a tool carries a filesystem reference. It is a
// property of the tool's contract, not of any benchmark: read and write name a
// file, glob names a directory, grep carries the text inline.
var pathArg = map[string]string{
	"read":  "ref",
	"write": "ref",
	"glob":  "pattern",
}

// Gate admits or refuses proposed calls deterministically.
//
// The model's job is to choose a tool and copy values out of the request. The gate
// holds what a 30M-parameter model cannot: the schema, and whether a copied value
// names anything real. A call that fails either is not a wrong answer to fix by
// training, it is unanswerable — the gate abstains rather than executing a guess,
// which is exactly the behaviour a fine-tuned adapter loses (HARNESS.md §9).
type Gate struct {
	tools map[string][]string
	index PathIndex
}

// NewGate snapshots the registry's input schemas and grounds them against index.
func NewGate(reg *Registry, index PathIndex) *Gate {
	shapes := make(map[string][]string)
	for _, tool := range reg.List() {
		names := make([]string, 0, len(tool.Schema.Input))
		for name := range tool.Schema.Input {
			names = append(names, name)
		}
		sort.Strings(names)
		shapes[tool.Name] = names
	}
	return &Gate{tools: shapes, index: index}
}

// Tools returns the registry's tool names, sorted.
func (g *Gate) Tools() []string {
	out := make([]string, 0, len(g.tools))
	for name := range g.tools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Check runs the four rules in order and stops at the first failure.
//
//  1. the tool exists in the registry
//  2. the argument keys equal the declared inputs — no invented key, none missing
//  3. every value appears verbatim in the request (a call may only copy)
//  4. a path-shaped value resolves: an existing file to read, an existing parent
//     directory to write into, an existing directory to glob
func (g *Gate) Check(query string, call Call) Decision {
	decision := Decision{Verdict: Abstain, Call: call}

	declared, ok := g.tools[call.Name]
	if !ok {
		decision.Reason = "unknown tool: " + call.Name
		decision.Checks = append(decision.Checks, Check{Rule: "tool_exists", Detail: call.Name})
		return decision
	}
	decision.Checks = append(decision.Checks, Check{Rule: "tool_exists", OK: true})

	if detail := g.keyMismatch(declared, call.Args); detail != "" {
		decision.Reason = detail
		decision.Checks = append(decision.Checks, Check{Rule: "schema", Detail: detail})
		return decision
	}
	decision.Checks = append(decision.Checks, Check{Rule: "schema", OK: true})

	for _, key := range declared {
		value, _ := call.Args[key].(string)
		if !strings.Contains(query, value) {
			decision.Reason = fmt.Sprintf("value %q is not in the request", value)
			decision.Checks = append(decision.Checks, Check{Rule: "grounding", Detail: key})
			return decision
		}
	}
	decision.Checks = append(decision.Checks, Check{Rule: "grounding", OK: true})

	if detail := g.resolve(call); detail != "" {
		decision.Reason = detail
		decision.Checks = append(decision.Checks, Check{Rule: "resolution", Detail: detail})
		return decision
	}
	decision.Checks = append(decision.Checks, Check{Rule: "resolution", OK: true})

	decision.Verdict = Accept
	decision.Reason = "ok"
	return decision
}

// CheckAll keeps every call that passes and reports whether anything survived.
// One refused call out of a pair must not discard the pair.
//
// It first asks whether the request is in reach of the tool set at all (Fits).
// A schema-valid call to an out-of-scope request is the failure mode the gate
// exists for: the model answers "what should I have for dinner" with a confident
// grep once it has been fine-tuned to always emit a call.
func (g *Gate) CheckAll(query string, calls []Call) (kept []Call, decisions []Decision) {
	if !g.Fits(query) {
		for _, call := range calls {
			decisions = append(decisions, Decision{
				Verdict: Abstain,
				Reason:  "the request names nothing this tool set can act on",
				Call:    call,
				Checks:  []Check{{Rule: "request_supported"}},
			})
		}
		if len(calls) == 0 {
			decisions = append(decisions, Decision{Verdict: Abstain,
				Reason: "the request names nothing this tool set can act on",
				Checks: []Check{{Rule: "request_supported"}}})
		}
		return nil, decisions
	}

	for _, call := range calls {
		decision := g.Check(query, call)
		decisions = append(decisions, decision)
		if decision.Verdict == Accept {
			kept = append(kept, call)
		}
	}
	return kept, decisions
}

// Fits reports whether the request names something one of these tools can act on:
// an existing path or directory (read, write, glob) or a quoted span to search
// (grep). Every tool in the registry consumes a filesystem reference or a literal
// string, so a request carrying neither is outside all of them — the question is
// about the request, not about what the model proposed.
func (g *Gate) Fits(query string) bool {
	for _, span := range quotedSpans(query) {
		if len(strings.TrimSpace(span)) >= 2 {
			return true
		}
	}
	if g.index == nil {
		return true
	}
	for _, token := range strings.Fields(query) {
		// A root-level file carries no slash, so every token is asked.
		token = strings.Trim(token, "\"'`,;:()[]{}.?!")
		if token == "" {
			continue
		}
		if g.index.HasPath(token) || g.index.HasDir(token) {
			return true
		}
		if dir := path.Dir(token); dir != "." && g.index.HasDir(dir) {
			return true
		}
	}
	return false
}

func quotedSpans(query string) []string {
	var spans []string
	for {
		start := strings.IndexByte(query, '"')
		if start < 0 {
			return spans
		}
		rest := query[start+1:]
		end := strings.IndexByte(rest, '"')
		if end < 0 {
			return spans
		}
		spans = append(spans, rest[:end])
		query = rest[end+1:]
	}
}

func (g *Gate) keyMismatch(declared []string, args map[string]any) string {
	for key := range args {
		if !contains(declared, key) {
			return "undeclared argument: " + key
		}
	}
	for _, key := range declared {
		value, ok := args[key].(string)
		if !ok || value == "" {
			return "missing argument: " + key
		}
	}
	return ""
}

func (g *Gate) resolve(call Call) string {
	key, ok := pathArg[call.Name]
	if !ok || g.index == nil {
		return ""
	}
	value, _ := call.Args[key].(string)

	switch call.Name {
	case "read":
		if !g.index.HasPath(value) {
			return fmt.Sprintf("no such path: %s", value)
		}
	case "write":
		// A write may create the file, so only its directory has to exist.
		dir := path.Dir(value)
		if dir == "." {
			dir = ""
		}
		if !g.index.HasDir(dir) {
			return fmt.Sprintf("no such directory: %s", value)
		}
	case "glob":
		dir := path.Dir(value)
		if dir == "." {
			dir = ""
		}
		if !g.index.HasDir(dir) && !g.index.HasPath(value) {
			return fmt.Sprintf("no such directory: %s", value)
		}
	}
	return ""
}

func contains(haystack []string, needle string) bool {
	for _, value := range haystack {
		if value == needle {
			return true
		}
	}
	return false
}
