package tools

import (
	"os"
	"path/filepath"
	"sync"
)

// FileStateCache tracks which files have been read and their modification
// times, enforcing a read-before-edit discipline: edit_file and write_file
// refuse to touch a file the model hasn't read this session, or that changed
// on disk after the last read, so a write never lands on content the model
// hasn't seen. One cache is shared by read/write/edit so the state stays
// consistent across the three tools.
type FileStateCache struct {
	mu      sync.Mutex
	entries map[string]int64 // absolute path → mtime (Unix millis)
}

// NewFileStateCache returns an empty cache.
func NewFileStateCache() *FileStateCache {
	return &FileStateCache{entries: make(map[string]int64)}
}

// Record stores the file mtime after a successful read.
func (c *FileStateCache) Record(filePath string, mtime int64) {
	abs := normalizePath(filePath)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[abs] = mtime
}

// Check verifies that a file has been read and hasn't changed on disk since.
// It returns (true, "") when the edit may proceed, or (false, message) with
// the refusal the model should see.
func (c *FileStateCache) Check(filePath string) (bool, string) {
	abs := normalizePath(filePath)
	c.mu.Lock()
	cachedMtime, exists := c.entries[abs]
	c.mu.Unlock()

	if !exists {
		return false, "file has not been read yet; read it with read_file first"
	}
	info, err := os.Stat(abs)
	if err != nil {
		// Deleted since the read — let the caller's own IO error speak.
		return true, ""
	}
	if info.ModTime().UnixMilli() > cachedMtime {
		return false, "file has been modified since it was last read; read it again before editing"
	}
	return true, ""
}

// Update refreshes the cached mtime after a successful edit or write.
func (c *FileStateCache) Update(filePath string) {
	abs := normalizePath(filePath)
	info, err := os.Stat(abs)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[abs] = info.ModTime().UnixMilli()
}

func normalizePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}
