//go:build windows

package session

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// isProcessRunning asks tasklist — no extra dependency, mirroring the probe
// mewcode uses. An unreadable answer counts as alive: a lock is never stolen
// on a guess.
func isProcessRunning(pid int) bool {
	out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH").Output()
	if err != nil {
		return true
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}
