package tools

import (
	"context"
	"strings"
	"testing"
)

type fakeTool struct {
	name string
}

func (f fakeTool) Name() string                                       { return f.name }
func (f fakeTool) Description() string                                { return "desc of " + f.name }
func (f fakeTool) Category() Category                                 { return CategoryRead }
func (f fakeTool) Schema() map[string]any                             { return toolSchema(f.name, f.Description(), fakeSchema()) }
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
	first := r.GetAllSchemas()
	second := r.GetAllSchemas()
	if len(first) != 2 {
		t.Fatalf("len = %d, want 2", len(first))
	}
	for i := range first {
		if first[i]["name"] != second[i]["name"] {
			t.Fatalf("order differs between calls: %v vs %v", first[i]["name"], second[i]["name"])
		}
	}
	if first[0]["name"] != "bash" || first[1]["name"] != "write_file" {
		t.Errorf("schemas not sorted by name: %v, %v", first[0]["name"], first[1]["name"])
	}
	s := first[0]
	if s["description"] != "desc of bash" {
		t.Errorf("description = %v", s["description"])
	}
	if _, ok := s["parameters"]; ok {
		t.Errorf("neutral schema must not carry the openai parameters key: %v", s)
	}
	schema, ok := s["input_schema"].(map[string]any)
	if !ok || schema["type"] != "object" {
		t.Errorf("input_schema = %v", s["input_schema"])
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
