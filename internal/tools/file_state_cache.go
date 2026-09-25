package tools

import (
	"os"
	"path/filepath"
	"sync"
)

// fileState is what a read recorded: size joins the modification time so an
// external write is caught even when the mtime moves backwards (a git
// checkout restores it) or lands in the same millisecond. A same-size write
// within one millisecond remains invisible — the marker's remaining gap.
type fileState struct {
	mtimeMillis int64
	size        int64
}

type FileStateCache struct {
	mu      sync.Mutex
	entries map[string]fileState
}

func NewFileStateCache() *FileStateCache {
	return &FileStateCache{entries: make(map[string]fileState)}
}

// Record notes a file's current state, whether it was just read or just
// written.
func (c *FileStateCache) Record(filePath string) {
	abs := normalizePath(filePath)
	info, err := os.Stat(abs)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[abs] = fileState{mtimeMillis: info.ModTime().UnixMilli(), size: info.Size()}
}

// Verify reports whether a write to the file may proceed — it must have been
// read and left unchanged since. An empty reason means it may.
func (c *FileStateCache) Verify(filePath string) string {
	abs := normalizePath(filePath)
	c.mu.Lock()
	cached, exists := c.entries[abs]
	c.mu.Unlock()

	if !exists {
		return "file has not been read yet; read it with Read first"
	}
	info, err := os.Stat(abs)
	if err != nil {
		// Deleted since the read — let the caller's own IO error speak.
		return ""
	}
	if info.ModTime().UnixMilli() != cached.mtimeMillis || info.Size() != cached.size {
		return "file has been modified since it was last read; read it again before editing"
	}
	return ""
}

// normalizePath resolves symlinks as far as they exist, mirroring the
// permission checker's resolution, so a file read through one path and
// edited through another is one entry. The ~10 lines are duplicated rather
// than imported: permissions already depends on this package.
func normalizePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	var rest string
	current := filepath.Clean(abs)
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(abs)
		}
		rest = filepath.Join(filepath.Base(current), rest)
		current = parent
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(resolved, rest)
		}
	}
}
