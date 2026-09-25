package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Info is what a session scan derives from the archive itself — there is no
// separate metadata file to keep in sync.
type Info struct {
	ID           string
	Title        string
	Cwd          string
	Messages     int
	UserMessages int
	Skipped      int // unreadable records; a torn tail counts once
	Dropped      int // interrupted tool turns removed on resume
	ModTime      time.Time
}

var idPattern = regexp.MustCompile(`^\d{8}-\d{6}-[0-9a-f]{4}$`)

func sanitizeID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("session: invalid session id %q", id)
	}
	return nil
}

// List scans the archives for one working directory, newest first. Anything
// unreadable is skipped rather than fatal; the caller decides what to do
// about a session it cannot resume.
func List(cwd string) ([]Info, error) {
	dir, err := sessionsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: cannot list %s: %w", dir, err)
	}
	var infos []Info
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(name, ".jsonl")
		if sanitizeID(id) != nil {
			continue
		}
		info, err := scanInfo(dir, id, entry)
		if err != nil {
			continue
		}
		if cwd != "" && info.Cwd != cwd {
			continue
		}
		infos = append(infos, info)
	}
	slices.SortFunc(infos, func(a, b Info) int { return b.ModTime.Compare(a.ModTime) })
	return infos, nil
}

func scanInfo(dir, id string, entry os.DirEntry) (Info, error) {
	info := Info{ID: id}
	if fi, err := entry.Info(); err == nil {
		info.ModTime = fi.ModTime()
	}
	f, err := os.Open(filepath.Join(dir, id+".jsonl"))
	if err != nil {
		return Info{}, err
	}
	defer f.Close()
	loaded, err := replay(f)
	if err != nil {
		return Info{}, err
	}
	info.Cwd = loaded.cwd
	info.Title = loaded.title
	info.Messages = loaded.messageCount
	info.UserMessages = loaded.userCount
	info.Skipped = loaded.skipped
	if !loaded.lastActive.IsZero() {
		info.ModTime = loaded.lastActive
	}
	return info, nil
}

// Cleanup deletes sessions whose archive has not been touched for maxAge —
// the archive, its lock and its spill directory go together. Best-effort and
// silent: it is housekeeping, and a session whose lock is held by a live
// process is never touched.
func Cleanup(maxAge time.Duration) int {
	dir, err := sessionsDir()
	if err != nil {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(name, ".jsonl")
		if sanitizeID(id) != nil {
			continue
		}
		fi, err := entry.Info()
		if err != nil || fi.ModTime().After(cutoff) {
			continue
		}
		lockPath := filepath.Join(dir, id+".lock")
		if pid, ok := readLockPID(lockPath); ok && isProcessRunning(pid) {
			continue
		}
		os.Remove(filepath.Join(dir, id+".jsonl"))
		os.Remove(lockPath)
		os.RemoveAll(filepath.Join(dir, id))
		removed++
	}
	return removed
}
