package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestFileStateCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := NewFileStateCache()

	if reason := c.Verify(path); reason == "" {
		t.Error("an unread file must be refused")
	}

	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	c.Record(path)
	if reason := c.Verify(path); reason != "" {
		t.Errorf("an unchanged file was refused: %s", reason)
	}

	// Same mtime, different size: the classic same-millisecond rewrite.
	if err := os.WriteFile(path, []byte("much longer content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	if reason := c.Verify(path); reason == "" {
		t.Error("a same-mtime resize must be caught by the size check")
	}

	// Older mtime, same size: a checkout restoring an old timestamp.
	if err := os.WriteFile(path, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	older := past.Add(-time.Hour)
	if err := os.Chtimes(path, older, older); err != nil {
		t.Fatal(err)
	}
	if reason := c.Verify(path); reason == "" {
		t.Error("an older mtime must be caught")
	}

	// Deleted since the read: the caller's own IO error speaks.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if reason := c.Verify(path); reason != "" {
		t.Errorf("a deleted file must pass here: %s", reason)
	}
}

func TestFileStateCacheSymlinkRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on windows")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	c := NewFileStateCache()
	c.Record(link) // read through the symlink
	if reason := c.Verify(real); reason != "" {
		t.Errorf("editing via the real path after a symlinked read was refused: %s", reason)
	}
}
