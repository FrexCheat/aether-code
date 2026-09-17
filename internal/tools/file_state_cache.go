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

func (c *FileStateCache) Record(filePath string, mtime int64) {
	abs := normalizePath(filePath)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[abs] = mtime
}

func (c *FileStateCache) Check(filePath string) (bool, string) {
	abs := normalizePath(filePath)
	c.mu.Lock()
	cachedMtime, exists := c.entries[abs]
	c.mu.Unlock()

	if !exists {
		return false, "file has not been read yet; read it with Read first"
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
