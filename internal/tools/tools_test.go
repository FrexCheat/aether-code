package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func execTool(t *testing.T, tool Tool, args map[string]any) ToolResult {
	t.Helper()
	return tool.Execute(context.Background(), args)
}

// --- read_file ---

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	content := "one\ntwo\nthree\nfour\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadFileTool{WorkDir: dir}

	got := execTool(t, tool, map[string]any{"file_path": "sample.txt"})
	if got.IsError || !strings.Contains(got.Output, "two") {
		t.Fatalf("read all = %+v", got)
	}
	if !strings.Contains(got.Output, "1\tone") && !strings.Contains(got.Output, "     1\tone") {
		t.Errorf("output missing line numbers: %q", got.Output)
	}

	paged := execTool(t, tool, map[string]any{"file_path": "sample.txt", "offset": 2.0, "limit": 1.0})
	if paged.IsError || !strings.Contains(paged.Output, "two") || strings.Contains(paged.Output, "one\n") {
		t.Errorf("paged read = %+v", paged)
	}

	missing := execTool(t, tool, map[string]any{"file_path": "nope.txt"})
	if !missing.IsError {
		t.Errorf("missing file should be an error result: %+v", missing)
	}
	dirRes := execTool(t, tool, map[string]any{"file_path": "."})
	if !dirRes.IsError {
		t.Errorf("reading a directory should be an error result: %+v", dirRes)
	}
	noArg := execTool(t, tool, map[string]any{})
	if !noArg.IsError {
		t.Errorf("missing arg should be an error result: %+v", noArg)
	}
}

// --- write_file ---

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	tool := &WriteFileTool{WorkDir: dir}

	got := execTool(t, tool, map[string]any{"file_path": "a/b/c.txt", "content": "hello\n"})
	if got.IsError {
		t.Fatalf("write = %+v", got)
	}
	data, err := os.ReadFile(filepath.Join(dir, "a/b/c.txt"))
	if err != nil || string(data) != "hello\n" {
		t.Fatalf("content = %q, err = %v", data, err)
	}

	over := execTool(t, tool, map[string]any{"file_path": "a/b/c.txt", "content": "bye"})
	if over.IsError {
		t.Fatalf("overwrite = %+v", over)
	}
	data, _ = os.ReadFile(filepath.Join(dir, "a/b/c.txt"))
	if string(data) != "bye" {
		t.Errorf("after overwrite = %q", data)
	}

	// No temp litter left behind.
	entries, _ := os.ReadDir(filepath.Join(dir, "a/b"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

// --- edit_file ---

func TestEditFileUniqueMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "code.txt")
	original := "alpha\nbeta\ngamma\ndelta\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &EditFileTool{WorkDir: dir}

	got := execTool(t, tool, map[string]any{"file_path": "code.txt", "old_string": "beta", "new_string": "BETA"})
	if got.IsError {
		t.Fatalf("edit = %+v", got)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "alpha\nBETA\ngamma\ndelta\n" {
		t.Errorf("file after edit = %q", data)
	}
	assertDiffLines(t, got.Output, map[string]string{"beta": "-", "BETA": "+"}, []string{"alpha", "delta"})
}

// assertDiffLines checks that each content line appears with the wanted
// marker ("-" or "+"), and that context lines carry no marker.
func assertDiffLines(t *testing.T, diff string, want map[string]string, context []string) {
	t.Helper()
	findMarker := func(text string) (string, bool) {
		for _, line := range strings.Split(diff, "\n") {
			if !strings.HasSuffix(strings.TrimRight(line, " \t"), text) {
				continue
			}
			switch {
			case strings.HasPrefix(line, "-"):
				return "-", true
			case strings.HasPrefix(line, "+"):
				return "+", true
			case strings.HasPrefix(line, " "):
				return " ", true
			}
		}
		return "", false
	}
	for text, marker := range want {
		got, ok := findMarker(text)
		if !ok || got != marker {
			t.Errorf("line %q: got marker %q (present=%v), want %q\ndiff:\n%s", text, got, ok, marker, diff)
		}
	}
	for _, text := range context {
		got, ok := findMarker(text)
		if !ok || got == "-" || got == "+" {
			t.Errorf("context line %q should be unmarked, got %q (present=%v)\ndiff:\n%s", text, got, ok, diff)
		}
	}
}

func TestEditFileZeroAndMultiMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "code.txt")
	original := "alpha\nbeta\nalpha\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &EditFileTool{WorkDir: dir}

	zero := execTool(t, tool, map[string]any{"file_path": "code.txt", "old_string": "missing", "new_string": "x"})
	if !zero.IsError || !strings.Contains(zero.Output, "not found") {
		t.Errorf("zero-match result = %+v", zero)
	}
	multi := execTool(t, tool, map[string]any{"file_path": "code.txt", "old_string": "alpha", "new_string": "x"})
	if !multi.IsError || !strings.Contains(multi.Output, "2 times") {
		t.Errorf("multi-match result = %+v", multi)
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Errorf("file was modified on failed edit: %q", data)
	}
}

func TestBuildDiff(t *testing.T) {
	oldStr := "a\nb\nc\nd\ne\nf\ng\n"
	newStr := "a\nb\nc\nX\ne\nf\ng\n"
	diff := BuildDiff(oldStr, newStr)
	assertDiffLines(t, diff.Text,
		map[string]string{"d": "-", "X": "+"}, []string{"c", "e", "f"})
	if diff.Additions != 1 || diff.Removals != 1 {
		t.Errorf("counts = +%d/-%d, want 1/1", diff.Additions, diff.Removals)
	}

	// Pure addition: nothing removed.
	diff = BuildDiff("a\nb\n", "a\nb\nc\n")
	assertDiffLines(t, diff.Text,
		map[string]string{"c": "+"}, []string{"a", "b"})
	if diff.Additions != 1 || diff.Removals != 0 {
		t.Errorf("counts = +%d/-%d, want 1/0", diff.Additions, diff.Removals)
	}
}

// --- bash ---

func TestBash(t *testing.T) {
	dir := t.TempDir()
	tool := &BashTool{WorkDir: dir}

	got := execTool(t, tool, map[string]any{"command": "echo hello-from-bash"})
	if got.IsError || !strings.Contains(got.Output, "hello-from-bash") {
		t.Fatalf("echo = %+v", got)
	}

	merged := execTool(t, tool, map[string]any{"command": "echo out; echo err 1>&2"})
	if merged.IsError || !strings.Contains(merged.Output, "out") || !strings.Contains(merged.Output, "err") {
		t.Errorf("merged output = %+v", merged)
	}

	// Non-zero exit is a result for the model, not a tool failure.
	// Command-not-found (exit 127) likewise rides in the output text.
	failed := execTool(t, tool, map[string]any{"command": "exit 3"})
	if failed.IsError {
		t.Errorf("non-zero exit should not be IsError: %+v", failed)
	}
	if !strings.Contains(failed.Output, "Exit code 3") {
		t.Errorf("non-zero exit output = %+v", failed)
	}
}

// TestBashExitCodeSemantics checks that ordinary-meaning exit codes come back
// with their explanation instead of looking like failures: grep's exit 1 is
// "no matches", and it must survive a pipeline (the last command decides).
func TestBashExitCodeSemantics(t *testing.T) {
	tool := &BashTool{WorkDir: t.TempDir()}

	noMatch := execTool(t, tool, map[string]any{"command": "echo hello | grep zzz"})
	if noMatch.IsError || !strings.Contains(noMatch.Output, "(no matches found)") {
		t.Errorf("grep no-match output = %+v", noMatch)
	}
	if !strings.Contains(noMatch.Output, "Exit code 1") {
		t.Errorf("grep no-match should still report the exit code: %+v", noMatch)
	}

	if got := extractLastCommand("cat file | grep foo"); got != "grep" {
		t.Errorf("extractLastCommand pipeline = %q", got)
	}
	if got := extractLastCommand("cat file | rg foo"); got != "rg" {
		t.Errorf("extractLastCommand pipeline = %q", got)
	}
	if interpretExitCode("grep x y", 1) {
		t.Errorf("grep exit 1 should not be an error")
	}
	if !interpretExitCode("grep x y", 2) {
		t.Errorf("grep exit 2 should be an error")
	}
}

// --- read-before-edit gate ---

func TestReadBeforeEditGate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "code.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fsc := NewFileStateCache()
	rt := &ReadFileTool{WorkDir: dir, FileStateCache: fsc}
	et := &EditFileTool{WorkDir: dir, FileStateCache: fsc}

	// Editing before any read is refused.
	blocked := execTool(t, et, map[string]any{"file_path": "code.txt", "old_string": "beta", "new_string": "BETA"})
	if !blocked.IsError || !strings.Contains(blocked.Output, "has not been read") {
		t.Fatalf("unread edit should be refused: %+v", blocked)
	}

	// After a read the same edit goes through.
	read := execTool(t, rt, map[string]any{"file_path": "code.txt"})
	if read.IsError {
		t.Fatalf("read = %+v", read)
	}
	ok := execTool(t, et, map[string]any{"file_path": "code.txt", "old_string": "beta", "new_string": "BETA"})
	if ok.IsError {
		t.Fatalf("edit after read = %+v", ok)
	}

	// An external change after the read is refused until the file is re-read.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	stale := execTool(t, et, map[string]any{"file_path": "code.txt", "old_string": "BETA", "new_string": "gamma"})
	if !stale.IsError || !strings.Contains(stale.Output, "modified since") {
		t.Fatalf("stale edit should be refused: %+v", stale)
	}
}

func TestWriteFileGate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	fsc := NewFileStateCache()
	wt := &WriteFileTool{WorkDir: dir, FileStateCache: fsc}

	// Creating a new file needs no prior read.
	first := execTool(t, wt, map[string]any{"file_path": "new/sub.txt", "content": "v1"})
	if first.IsError {
		t.Fatalf("new-file write = %+v", first)
	}

	// Overwriting a file that appeared without being read is refused.
	if err := os.WriteFile(path, []byte("external"), 0o644); err != nil {
		t.Fatal(err)
	}
	blocked := execTool(t, wt, map[string]any{"file_path": "a.txt", "content": "v2"})
	if !blocked.IsError || !strings.Contains(blocked.Output, "has not been read") {
		t.Fatalf("unread overwrite should be refused: %+v", blocked)
	}

	// After a read it succeeds.
	rt := &ReadFileTool{WorkDir: dir, FileStateCache: fsc}
	if execTool(t, rt, map[string]any{"file_path": "a.txt"}).IsError {
		t.Fatal("read failed")
	}
	ok := execTool(t, wt, map[string]any{"file_path": "a.txt", "content": "v2"})
	if ok.IsError {
		t.Fatalf("overwrite after read = %+v", ok)
	}
}

// TestReadFileDefaultLimit pins the 2000-line default promised in the
// description, with the continuation hint pointing at the next offset.
func TestReadFileDefaultLimit(t *testing.T) {
	dir := t.TempDir()
	lines := make([]string, 2500)
	for i := range lines {
		lines[i] = "line"
	}
	path := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &ReadFileTool{WorkDir: dir}

	got := execTool(t, tool, map[string]any{"file_path": "big.txt"})
	if got.IsError {
		t.Fatalf("read = %+v", got)
	}
	if n := strings.Count(got.Output, "line\n"); n != 2000 {
		t.Errorf("default read returned %d lines, want 2000", n)
	}
	if !strings.Contains(got.Output, "500 more lines") || !strings.Contains(got.Output, "offset=2001") {
		t.Errorf("missing continuation hint: %q", got.Output[len(got.Output)-120:])
	}
}

func TestBashTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("timeout test")
	}
	tool := &BashTool{WorkDir: t.TempDir()}
	start := time.Now()
	got := execTool(t, tool, map[string]any{"command": "sleep 30", "timeout": 1.0})
	if !got.IsError || !strings.Contains(got.Output, "timed out") {
		t.Fatalf("timeout result = %+v", got)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("timeout took %v, should be ~1s", elapsed)
	}
}

// --- glob ---

func TestGlob(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{
		"main.go",
		"internal/a/a.go",
		"internal/b/b.go",
		"node_modules/x/index.js",
		".git/hidden.go",
		"docs/readme.md",
	} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tool := &GlobTool{WorkDir: dir}

	all := execTool(t, tool, map[string]any{"pattern": "**/*.go"})
	if all.IsError {
		t.Fatalf("glob = %+v", all)
	}
	for _, want := range []string{"main.go", "internal/a/a.go", "internal/b/b.go"} {
		if !strings.Contains(all.Output, want) {
			t.Errorf("output missing %s: %q", want, all.Output)
		}
	}
	for _, banned := range []string{"node_modules", ".git"} {
		if strings.Contains(all.Output, banned) {
			t.Errorf("output leaked ignored dir %s: %q", banned, all.Output)
		}
	}

	single := execTool(t, tool, map[string]any{"pattern": "*.md", "path": "docs"})
	if single.IsError || !strings.Contains(single.Output, "readme.md") {
		t.Errorf("path-scoped glob = %+v", single)
	}

	none := execTool(t, tool, map[string]any{"pattern": "*.rs"})
	if none.IsError || !strings.Contains(none.Output, "no files match") {
		t.Errorf("empty glob = %+v", none)
	}
}

// --- grep ---

func TestGrep(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"a/x.go", "b/y.js"} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a/x.go"), []byte("package a\n\nfunc Hello() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b/y.js"), []byte("function Hello() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &GrepTool{WorkDir: dir}

	got := execTool(t, tool, map[string]any{"pattern": `func \w+\(\)`})
	if got.IsError || !strings.Contains(got.Output, "a/x.go:3: func Hello() {}") {
		t.Fatalf("grep = %+v", got)
	}

	filtered := execTool(t, tool, map[string]any{"pattern": "Hello", "include": "*.js"})
	if filtered.IsError || !strings.Contains(filtered.Output, "b/y.js:1") || strings.Contains(filtered.Output, "x.go") {
		t.Errorf("include-filtered grep = %+v", filtered)
	}

	scoped := execTool(t, tool, map[string]any{"pattern": "Hello", "path": "a"})
	if scoped.IsError || strings.Contains(scoped.Output, "y.js") {
		t.Errorf("path-scoped grep = %+v", scoped)
	}

	invalid := execTool(t, tool, map[string]any{"pattern": "(unclosed"})
	if !invalid.IsError || !strings.Contains(invalid.Output, "invalid regular expression") {
		t.Errorf("invalid regex = %+v", invalid)
	}

	none := execTool(t, tool, map[string]any{"pattern": "zzz-not-there"})
	if none.IsError || !strings.Contains(none.Output, "no matches") {
		t.Errorf("no-match grep = %+v", none)
	}
}

// --- registry filtering ---

func TestFilteredSchemas(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&ReadFileTool{})
	reg.Register(&WriteFileTool{})
	reg.Register(&EditFileTool{})
	reg.Register(&BashTool{})
	reg.Register(&GlobTool{})
	reg.Register(&GrepTool{})

	// Read-only filter yields exactly the three read tools, name-sorted.
	read := reg.FilteredSchemas(func(c Category) bool { return c == CategoryRead })
	if len(read) != 3 {
		t.Fatalf("read schemas = %d entries, want 3", len(read))
	}
	wantOrder := []string{"glob", "grep", "read_file"}
	for i, w := range wantOrder {
		if got := read[i].Name; got != w {
			t.Errorf("read[%d].Name = %v, want %s", i, got, w)
		}
	}

	// Include-all filter is equivalent to GetAllSchemas.
	all := reg.FilteredSchemas(func(Category) bool { return true })
	if len(all) != 6 {
		t.Fatalf("all schemas = %d entries, want 6", len(all))
	}
	full := reg.GetAllSchemas()
	if len(full) != len(all) {
		t.Fatalf("GetAllSchemas = %d entries, want %d", len(full), len(all))
	}
	for i := range full {
		if full[i].Name != all[i].Name {
			t.Errorf("entry %d: GetAllSchemas=%v FilteredSchemas=%v", i, full[i].Name, all[i].Name)
		}
	}

	// Empty filter yields an empty (non-nil) list.
	none := reg.FilteredSchemas(func(Category) bool { return false })
	if len(none) != 0 {
		t.Fatalf("empty filter = %d entries, want 0", len(none))
	}
}
