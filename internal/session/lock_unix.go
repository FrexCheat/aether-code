//go:build unix

package session

import "syscall"

// isProcessRunning probes with signal 0: EPERM means the process exists but
// belongs to another user, which still counts as alive.
func isProcessRunning(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
