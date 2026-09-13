package tools

import (
	"context"
	"strings"
	"testing"
)

type fakeTool struct {
	name string
}

func (f fakeTool) Name() string        { return f.name }
func (f fakeTool) Description() string { return "desc of " + f.name }
func (f fakeTool) Category() Category  { return CategoryRead }
func (f fakeTool) Schema() ToolSchema {
	return ToolSchema{Name: f.name, Description: f.Description(), InputSchema: fakeSchema()}
}
func (f fakeTool) Execute(context.Context, map[string]any) ToolResult { return ToolResult{} }

func fakeSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
		"required": []string{"path"},
	}
}

func TestRegistryRegisterGet(t *testing.T) {
	r := NewRegistry()
	if r.Get("read_file") != nil {
		t.Fatal("empty registry returned a tool")
	}
	r.Register(fakeTool{name: "read_file"})
	r.Register(fakeTool{name: "bash"})
	if r.Get("read_file") == nil || r.Get("bash") == nil {
		t.Fatal("Get failed for registered tools")
	}
	if r.Get("nope") != nil {
		t.Fatal("Get returned a tool for unknown name")
	}
}

func TestRegistryDuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate registration did not panic")
		}
	}()
	r := NewRegistry()
	r.Register(fakeTool{name: "x"})
	r.Register(fakeTool{name: "x"})
}

func TestRegistrySchemasNeutral(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeTool{name: "write_file"})
	r.Register(fakeTool{name: "bash"})

	// Byte stability across calls: same set, same order, both times.
	first := r.Schemas(nil)
	second := r.Schemas(nil)
	if len(first) != 2 {
		t.Fatalf("len = %d, want 2", len(first))
	}
	for i := range first {
		if first[i].Name != second[i].Name {
			t.Fatalf("order differs between calls: %v vs %v", first[i].Name, second[i].Name)
		}
	}
	if first[0].Name != "bash" || first[1].Name != "write_file" {
		t.Errorf("schemas not sorted by name: %v, %v", first[0].Name, first[1].Name)
	}
	s := first[0]
	if s.Description != "desc of bash" {
		t.Errorf("description = %v", s.Description)
	}
	schema := s.InputSchema
	if schema["type"] != "object" {
		t.Errorf("input_schema = %v", s.InputSchema)
	}
}

func TestTruncateTitle(t *testing.T) {
	short := "go test ./..."
	if truncateTitle(short, 50) != short {
		t.Fatal("short title modified")
	}
	// Multi-byte runes are cut whole, never mid-sequence.
	long := strings.Repeat("中", 60)
	got := truncateTitle(long, 50)
	runes := []rune(strings.TrimSuffix(got, "…"))
	if len(runes) != 50 || !strings.HasSuffix(got, "…") {
		t.Errorf("truncateTitle = %d runes, want 50 + ellipsis", len(runes))
	}
}

func TestTruncateOutput(t *testing.T) {
	short := "hello"
	if TruncateOutput(short, MaxOutputChars) != short {
		t.Fatal("short output modified")
	}
	long := strings.Repeat("x", MaxOutputChars+100)
	got := TruncateOutput(long, MaxOutputChars)
	if len(got) <= MaxOutputChars || !strings.Contains(got, "truncated") {
		t.Errorf("long output not truncated/marked: len=%d", len(got))
	}
}

func TestResolveWorkPath(t *testing.T) {
	cases := []struct{ workDir, path, want string }{
		{"/work", "a/b.go", "/work/a/b.go"},
		{"/work", "./a/../b.go", "/work/b.go"},
		{"/work", "/abs/x.go", "/abs/x.go"},
	}
	for _, c := range cases {
		if got := ResolveWorkPath(c.workDir, c.path); got != c.want {
			t.Errorf("ResolveWorkPath(%q, %q) = %q, want %q", c.workDir, c.path, got, c.want)
		}
	}
}
