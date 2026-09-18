package tools

import (
	"os"
	"path/filepath"
	"sync"
)

type FileStateCache struct {
	mu      sync.Mutex
	entries map[string]int64 // absolute path → mtime (Unix millis)
}

func NewFileStateCache() *FileStateCache {
	return &FileStateCache{entries: make(map[string]int64)}
}

// Record notes a file's current modification time, whether it was just read or
// just written.
func (c *FileStateCache) Record(filePath string) {
	abs := normalizePath(filePath)
	info, err := os.Stat(abs)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[abs] = info.ModTime().UnixMilli()
}

// Verify reports whether a write to the file may proceed — it must have been
// read and left unchanged since. An empty reason means it may.
func (c *FileStateCache) Verify(filePath string) string {
	abs := normalizePath(filePath)
	c.mu.Lock()
	cachedMtime, exists := c.entries[abs]
	c.mu.Unlock()

	if !exists {
		return "file has not been read yet; read it with Read first"
	}
	info, err := os.Stat(abs)
	if err != nil {
		// Deleted since the read — let the caller's own IO error speak.
		return ""
	}
	if info.ModTime().UnixMilli() > cachedMtime {
		return "file has been modified since it was last read; read it again before editing"
	}
	return ""
}

func normalizePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}
