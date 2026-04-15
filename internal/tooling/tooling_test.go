package tooling

import (
	"context"
	"testing"
)

func TestRegisterAndGet(t *testing.T) {
	reg := NewRegistry()

	tool := Tool{
		Name:        "test_tool",
		Description: "A test tool",
		Type:        ToolTypeRead,
		Handler: func(ctx context.Context, args map[string]any) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	}

	if err := reg.Register(tool); err != nil {
		t.Fatal(err)
	}

	got, ok := reg.Get("test_tool")
	if !ok {
		t.Fatal("expected tool to exist")
	}
	if got.Name != "test_tool" {
		t.Fatalf("expected name test_tool, got %s", got.Name)
	}
}

func TestRegisterEmptyName(t *testing.T) {
	reg := NewRegistry()

	tool := Tool{Name: "", Description: "empty", Handler: func(ctx context.Context, args map[string]any) (map[string]any, error) {
		return nil, nil
	}}

	if err := reg.Register(tool); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestRegisterNoHandler(t *testing.T) {
	reg := NewRegistry()

	tool := Tool{Name: "no_handler", Description: "no handler", Type: ToolTypeRead}

	if err := reg.Register(tool); err == nil {
		t.Fatal("expected error for tool without handler")
	}
}

func TestListSorted(t *testing.T) {
	reg := NewRegistry()

	reg.Register(Tool{Name: "zebra", Type: ToolTypeRead, Handler: dummyHandler})
	reg.Register(Tool{Name: "alpha", Type: ToolTypeRead, Handler: dummyHandler})
	reg.Register(Tool{Name: "middle", Type: ToolTypeRead, Handler: dummyHandler})

	names := reg.ListNames()
	if len(names) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(names))
	}
	if names[0] != "alpha" || names[1] != "middle" || names[2] != "zebra" {
		t.Fatalf("expected alphabetical order, got %v", names)
	}
}

func TestExecute(t *testing.T) {
	reg := NewRegistry()

	reg.Register(Tool{
		Name: "echo",
		Type: ToolTypeRead,
		Handler: func(ctx context.Context, args map[string]any) (map[string]any, error) {
			return args, nil
		},
	})

	result, err := reg.Execute(context.Background(), "echo", map[string]any{"msg": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ToolName != "echo" {
		t.Fatalf("expected tool name echo, got %s", result.ToolName)
	}
	if result.Result["msg"] != "hello" {
		t.Fatalf("expected msg=hello, got %v", result.Result["msg"])
	}
}

func TestExecuteNotFound(t *testing.T) {
	reg := NewRegistry()

	_, err := reg.Execute(context.Background(), "nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for missing tool")
	}
}

func TestExecuteManyReadsParallel(t *testing.T) {
	reg := NewRegistry()

	reg.Register(Tool{Name: "r1", Type: ToolTypeRead, Handler: dummyHandler})
	reg.Register(Tool{Name: "r2", Type: ToolTypeRead, Handler: dummyHandler})

	requests := []ToolRequest{
		{Name: "r1"},
		{Name: "r2"},
	}

	results := reg.ExecuteMany(context.Background(), requests)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestBuiltinToolsRegistered(t *testing.T) {
	reg := NewRegistry()
	RegisterBuiltinTools(reg)

	names := reg.ListNames()
	expected := []string{"glob", "grep", "read", "write"}
	if len(names) != len(expected) {
		t.Fatalf("expected %d builtin tools, got %d: %v", len(expected), len(names), names)
	}
	for i, name := range expected {
		if names[i] != name {
			t.Fatalf("expected %q at position %d, got %q", name, i, names[i])
		}
	}
}

func TestSortTools(t *testing.T) {
	names := []string{"zebra", "alpha", "middle"}
	SortTools(names)
	if names[0] != "alpha" || names[1] != "middle" || names[2] != "zebra" {
		t.Fatalf("expected alphabetical order, got %v", names)
	}
}

func dummyHandler(ctx context.Context, args map[string]any) (map[string]any, error) {
	return map[string]any{"ok": true}, nil
}
