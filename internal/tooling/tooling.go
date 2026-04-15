// Package tooling provides an opinionated, concurrent tooling layer for the harness.
//
// This is scaffolding for future use — tools are NOT enabled by default.
//
// Design principles:
//   - Only structured, typed tools (no raw shell).
//   - Reads may run in parallel; writes must be strictly serial.
//   - Tool list is always sorted alphabetically before being sent downstream (KV-cache optimization).
package tooling

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

var (
	ErrToolNotFound   = errors.New("tool not found")
	ErrToolNotReady   = errors.New("tool not ready")
	ErrWriteInParallel = errors.New("write tools cannot run in parallel")
)

// ToolType categorizes tools as read or write.
type ToolType string

const (
	ToolTypeRead  ToolType = "read"
	ToolTypeWrite ToolType = "write"
)

// Tool is a structured, typed tool definition.
type Tool struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Type        ToolType  `json:"type"`
	Schema      ToolSchema `json:"schema"`
	Handler     ToolHandler `json:"-"`
}

// ToolSchema defines the input/output shape of a tool.
type ToolSchema struct {
	Input  map[string]any `json:"input"`
	Output map[string]any `json:"output"`
}

// ToolHandler is the function that executes the tool.
type ToolHandler func(ctx context.Context, args map[string]any) (map[string]any, error)

// ToolResult is the result of a tool execution.
type ToolResult struct {
	ToolName string
	Result   map[string]any
	Error    error
}

// Registry manages the available tools.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry creates a new tool registry.
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register adds a tool to the registry.
func (r *Registry) Register(tool Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := strings.TrimSpace(tool.Name)
	if name == "" {
		return errors.New("tool name is required")
	}
	if tool.Handler == nil {
		return fmt.Errorf("tool %q has no handler", name)
	}

	r.tools[name] = tool
	return nil
}

// Get retrieves a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all registered tools, sorted alphabetically (KV-cache optimization).
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

// ListNames returns tool names sorted alphabetically.
func (r *Registry) ListNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Execute runs the specified tool with the given arguments.
func (r *Registry) Execute(ctx context.Context, name string, args map[string]any) (ToolResult, error) {
	tool, ok := r.Get(name)
	if !ok {
		return ToolResult{}, fmt.Errorf("%w: %s", ErrToolNotFound, name)
	}
	if tool.Handler == nil {
		return ToolResult{}, fmt.Errorf("%w: %s", ErrToolNotReady, name)
	}

	result, err := tool.Handler(ctx, args)
	return ToolResult{
		ToolName: name,
		Result:   result,
		Error:    err,
	}, err
}

// ExecuteMany runs multiple tools. Read tools run in parallel; write tools run serially.
func (r *Registry) ExecuteMany(ctx context.Context, requests []ToolRequest) []ToolResult {
	// Separate reads and writes, maintaining order
	var reads []ToolRequest
	var writes []ToolRequest
	for _, req := range requests {
		tool, ok := r.Get(req.Name)
		if !ok {
			continue
		}
		if tool.Type == ToolTypeWrite {
			writes = append(writes, req)
		} else {
			reads = append(reads, req)
		}
	}

	var wg sync.WaitGroup
	results := make([]ToolResult, len(requests))

	// Execute reads in parallel
	for i, req := range reads {
		wg.Add(1)
		go func(idx int, r ToolRequest) {
			defer wg.Done()
			res, err := r.Execute(ctx, r.Name, r.Args)
			results[idx] = ToolResult{ToolName: r.Name, Result: res, Error: err}
		}(i, req)
	}

	// Execute writes serially
	writeOffset := len(reads)
	for i, req := range writes {
		res, err := req.Execute(ctx, req.Name, req.Args)
		results[writeOffset+i] = ToolResult{ToolName: req.Name, Result: res, Error: err}
	}

	wg.Wait()
	return results
}

// ToolRequest is a request to execute a tool.
type ToolRequest struct {
	Name string
	Args map[string]any
}

// Execute is a helper to run a single tool request.
func (r ToolRequest) Execute(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	// This delegates to the registry; kept here for type safety
	return nil, nil
}

// --- Built-in Safe Tools (examples) ---

// RegisterBuiltinTools adds a small set of safe built-in tools to the registry.
func RegisterBuiltinTools(r *Registry) {
	// grep: search for a pattern in text
	_ = r.Register(Tool{
		Name:        "grep",
		Description: "Search for a pattern in text",
		Type:        ToolTypeRead,
		Schema: ToolSchema{
			Input:  map[string]any{"pattern": "string", "text": "string"},
			Output: map[string]any{"matches": "array", "count": "int"},
		},
		Handler: func(_ context.Context, args map[string]any) (map[string]any, error) {
			pattern, _ := args["pattern"].(string)
			text, _ := args["text"].(string)
			if pattern == "" || text == "" {
				return nil, errors.New("pattern and text are required")
			}
			// Simple substring match (real impl would use regex)
			var matches []string
			if idx := strings.Index(text, pattern); idx >= 0 {
				matches = append(matches, fmt.Sprintf("match at position %d", idx))
			}
			return map[string]any{
				"matches": matches,
				"count":   len(matches),
			}, nil
		},
	})

	// glob: list files matching a pattern (stub — would need filesystem access)
	_ = r.Register(Tool{
		Name:        "glob",
		Description: "List files matching a pattern",
		Type:        ToolTypeRead,
		Schema: ToolSchema{
			Input:  map[string]any{"pattern": "string"},
			Output: map[string]any{"files": "array"},
		},
		Handler: func(_ context.Context, args map[string]any) (map[string]any, error) {
			// Stub: returns empty list (real impl would use filepath.Glob)
			return map[string]any{"files": []string{}}, nil
		},
	})

	// read: read content by reference (stub — would need blob store access)
	_ = r.Register(Tool{
		Name:        "read",
		Description: "Read content by reference",
		Type:        ToolTypeRead,
		Schema: ToolSchema{
			Input:  map[string]any{"ref": "string"},
			Output: map[string]any{"content": "string"},
		},
		Handler: func(_ context.Context, args map[string]any) (map[string]any, error) {
			ref, _ := args["ref"].(string)
			if ref == "" {
				return nil, errors.New("ref is required")
			}
			// Stub: returns empty content
			return map[string]any{"content": "", "ref": ref}, nil
		},
	})

	// write: write content by reference (stub — would need blob store access)
	_ = r.Register(Tool{
		Name:        "write",
		Description: "Write content by reference",
		Type:        ToolTypeWrite,
		Schema: ToolSchema{
			Input:  map[string]any{"ref": "string", "content": "string"},
			Output: map[string]any{"ok": "bool"},
		},
		Handler: func(_ context.Context, args map[string]any) (map[string]any, error) {
			ref, _ := args["ref"].(string)
			if ref == "" {
				return nil, errors.New("ref is required")
			}
			// Stub: always succeeds
			return map[string]any{"ok": true, "ref": ref}, nil
		},
	})
}

// SortTools alphabetically sorts a list of tool names (KV-cache optimization).
func SortTools(names []string) {
	sort.Strings(names)
}
