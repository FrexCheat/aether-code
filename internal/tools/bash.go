package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// BashDefaultTimeout bounds one command when the model doesn't ask for more.
const BashDefaultTimeout = 120 * time.Second

// BashMaxTimeout is the ceiling for a caller-requested timeout.
const BashMaxTimeout = 10 * time.Minute

// commandErrorThresholds gives special commands the exit code at or above
// which a non-zero exit really means failure. grep exiting 1 means "no
// matches", not an error — only 2 is a syntax/IO error. diff, find and test
// likewise give exit 1 an ordinary meaning.
var commandErrorThresholds = map[string]int{
	"grep": 2,
	"rg":   2,
	"diff": 2,
	"find": 2,
	"test": 2,
	"[":    2,
}

// interpretExitCode reports whether a non-zero exit code represents a real
// failure. Pipelines resolve to their last command, matching how sh computes
// a pipeline's own exit status.
func interpretExitCode(command string, exitCode int) bool {
	if exitCode == 0 {
		return false
	}
	if threshold, ok := commandErrorThresholds[extractLastCommand(command)]; ok {
		return exitCode >= threshold
	}
	return true
}

// extractLastCommand returns the base name of the final command in a
// pipeline: "cat file | grep foo" → "grep".
func extractLastCommand(command string) string {
	parts := strings.Split(command, "|")
	fields := strings.Fields(strings.TrimSpace(parts[len(parts)-1]))
	if len(fields) == 0 {
		return ""
	}
	return filepath.Base(fields[0])
}

// exitCodeHint gives the semantic meaning of a non-error non-zero exit, so
// the model learns "no matches found" instead of an opaque failure line.
func exitCodeHint(command string, exitCode int) string {
	switch extractLastCommand(command) {
	case "grep", "rg":
		if exitCode == 1 {
			return "no matches found"
		}
	case "diff":
		if exitCode == 1 {
			return "files differ"
		}
	case "find":
		if exitCode == 1 {
			return "some directories were inaccessible"
		}
	case "test", "[":
		if exitCode == 1 {
			return "condition is false"
		}
	}
	return fmt.Sprintf("command failed with exit code %d", exitCode)
}

// BashTool runs one shell command via sh -c in the session working directory.
// stdout and stderr are captured combined so interleaved output reaches the
// model in order. A non-zero exit is NOT a tool failure — the command's own
// diagnostics are the result the model needs to decide what to do next; only
// infrastructure problems (spawn failure, timeout) are marked IsError, and
// commands whose exit 1 carries ordinary meaning (grep, diff, find, test)
// get that meaning spelled out instead of a bare failure line.
type BashTool struct {
	WorkDir string
}

func (t *BashTool) Name() string { return "bash" }
func (t *BashTool) Description() string {
	return BashDescription
}
func (t *BashTool) Category() Category { return CategoryCommand }

func (t *BashTool) Schema() ToolSchema {
	return ToolSchema{
		Name:        t.Name(),
		Description: t.Description(),
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The shell command to execute",
				},
				"timeout": map[string]any{
					"type":        "integer",
					"description": "Timeout in seconds (default 120, max 600)",
				},
			},
			"required": []string{"command"},
		},
	}
}

// titleCmdLimit caps the command shown in a tool entry's one-line title.
const titleCmdLimit = 50

// Title summarizes the call for the UI: "Bash: go test ./...". Long commands
// are cut at titleCmdLimit runes.
func (t *BashTool) Title(args map[string]any) string {
	cmd, _ := args["command"].(string)
	if cmd == "" {
		return ""
	}
	return "Bash: " + truncateTitle(cmd, titleCmdLimit)
}

func (t *BashTool) Execute(ctx context.Context, args map[string]any) ToolResult {
	command, ok := argString(args, "command")
	if !ok {
		return errResult("bash: missing required string argument \"command\"")
	}
	seconds, err := argInt(args, "timeout")
	if err != nil {
		return errResult("bash: " + err.Error())
	}
	timeout := BashDefaultTimeout
	if seconds > 0 {
		timeout = min(time.Duration(seconds)*time.Second, BashMaxTimeout)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "sh", "-c", command)
	cmd.Dir = t.WorkDir
	// Run in its own process group and kill the whole group on timeout: sh -c
	// can fork children (pipelines, background jobs) that would otherwise
	// outlive the kill and hold the output pipe open. WaitDelay bounds the
	// wait for those descendants' I/O after the group is gone.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 3 * time.Second
	output, err := cmd.CombinedOutput()

	// The echoed command and exit-code line frame the raw output so the model
	// can tell which invocation produced it and whether it failed.
	var sb bytes.Buffer
	fmt.Fprintf(&sb, "$ %s\n", command)
	if len(output) > 0 {
		sb.Write(output)
		if output[len(output)-1] != '\n' {
			sb.WriteByte('\n')
		}
	}
	exitCode := 0
	if exitErr, isExit := err.(*exec.ExitError); isExit {
		exitCode = exitErr.ExitCode()
	}
	if exitCode != 0 {
		fmt.Fprintf(&sb, "Exit code %d", exitCode)
		if !interpretExitCode(command, exitCode) {
			fmt.Fprintf(&sb, " (%s)", exitCodeHint(command, exitCode))
		}
	}
	out := TruncateOutput(sb.String(), MaxOutputChars)

	if runCtx.Err() == context.DeadlineExceeded {
		return errResult(fmt.Sprintf(
			"bash: command timed out after %s and was killed; output so far:\n%s", timeout, out))
	}
	if err != nil {
		if _, isExit := err.(*exec.ExitError); !isExit {
			return errResult(fmt.Sprintf("bash: failed to run command: %v", err))
		}
	}
	return ToolResult{Output: out}
}
