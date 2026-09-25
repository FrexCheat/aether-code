// Package memory owns the project instruction chain (AETHER.md), the
// auto-memory notes with their MEMORY.md index, and the background extraction
// pass that turns finished conversation turns into notes.
package memory

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
)

// contextMarker opens the injected session-context reminder: the stable first
// line the injector and the agent's presence probe share. It must not change
// without both sides changing.
const contextMarker = "记忆与项目指令（AETHER.md 与历史笔记索引）："

// aetherContextLimit caps the AETHER.md chain as injected with the session
// context; the extraction prompt re-reads the same chain with a smaller cap.
const aetherContextLimit = 48 * 1024

type Manager struct {
	client llm.Client
	cwd    string
	root   string // this project's note directory; "" when no home resolved

	mu   sync.Mutex
	busy bool
}

func NewManager(client llm.Client, cwd string) *Manager {
	return &Manager{client: client, cwd: cwd, root: Dir(cwd)}
}

// Dir is the memory directory of one working directory:
// <aether root>/memory/<sanitized-cwd>, the sanitized path keeping one
// project's notes reachable from anywhere.
func Dir(cwd string) string {
	root := config.DataRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "memory", sanitizeProject(cwd))
}

// sanitizeProject turns an absolute working directory into a directory name:
// every run of characters outside [A-Za-z0-9] collapses to one dash.
func sanitizeProject(cwd string) string {
	var b strings.Builder
	lastDash := true // drops a leading separator too
	for _, r := range cwd {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// SessionContext renders the reminder injected at session start: the
// AETHER.md chain, the memory index, and the rules for using the notes. Its
// first line is contextMarker; both are empty when there is nothing to say.
func (m *Manager) SessionContext() (marker, text string) {
	instructions := loadAetherChain(m.cwd, aetherContextLimit)
	index := m.index()
	if instructions == "" && index == "" {
		return "", ""
	}
	var b strings.Builder
	b.WriteString(contextMarker)
	b.WriteString("\n")
	if instructions != "" {
		b.WriteString("\n【项目指令】\n")
		b.WriteString(instructions)
		b.WriteString("\n")
	}
	if index != "" {
		b.WriteString("\n【记忆索引】\n")
		b.WriteString(index)
		fmt.Fprintf(&b, "\n使用说明：索引每一条对应 %s 下的一个文件，需要完整内容时用 Read 工具读取。", m.root)
		b.WriteString("笔记是此前会话的结论，可能过时或错误：依赖之前先用当前代码与文件核对，冲突时以代码为准。不要修改这些文件。")
	}
	return contextMarker, b.String()
}

// Inject appends the session-context reminder unless it is already in the
// history; reports whether it added one.
func (m *Manager) Inject(conv *conversation.Manager) (bool, error) {
	marker, text := m.SessionContext()
	return conversation.EnsureReminder(conv, marker, text)
}

// index reads MEMORY.md as-is; a missing index means no notes yet.
func (m *Manager) index() string {
	if m.root == "" {
		return ""
	}
	data, err := readIndexFile(m.root)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
