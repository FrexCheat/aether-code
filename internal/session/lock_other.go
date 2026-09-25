//go:build !unix && !windows

package session

// isProcessRunning cannot probe on this platform; reporting "alive" keeps the
// lock conservative — a stale lock is removed by hand rather than stolen from
// a running process.
func isProcessRunning(pid int) bool { return pid > 0 }
