package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrInUse reports that a live process holds the session's lock.
var ErrInUse = errors.New("session: in use")

// acquireLock takes the session lock file (its content is the holder's PID).
// The create is atomic, so two processes racing for the same session cannot
// both win; an existing lock naming a dead process is what a crash leaves
// behind, and dropping it is how --continue takes over without manual
// cleanup. A lock naming a live process is never stolen, by age or otherwise.
func acquireLock(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("session: cannot create %s: %w", filepath.Dir(path), err)
	}
	for range 3 {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, werr := f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
			cerr := f.Close()
			if werr != nil {
				os.Remove(path)
				return fmt.Errorf("session: cannot write %s: %w", path, werr)
			}
			if cerr != nil {
				os.Remove(path)
				return fmt.Errorf("session: cannot write %s: %w", path, cerr)
			}
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("session: cannot create %s: %w", path, err)
		}
		if pid, ok := readLockPID(path); ok && isProcessRunning(pid) {
			return fmt.Errorf("%w (%s held by pid %d)", ErrInUse, path, pid)
		}
		os.Remove(path)
	}
	return fmt.Errorf("%w (%s)", ErrInUse, path)
}

// releaseLock removes the lock only while it still names this process, so a
// lock another process took over is never deleted from under it.
func releaseLock(path string) {
	if pid, ok := readLockPID(path); ok && pid == os.Getpid() {
		os.Remove(path)
	}
}

func readLockPID(path string) (int, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}
