package tui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/FrexCheat/aether-code/internal/command"
	"github.com/FrexCheat/aether-code/internal/compaction"
	"github.com/FrexCheat/aether-code/internal/permissions"
	"github.com/FrexCheat/aether-code/internal/session"
)

// hostAdapter is the Model as a command.Host: the renderer-agnostic surface a
// command talks to. Effects the window can only perform asynchronously — a sent
// message, a compaction, a quit — are recorded here and turned into tea.Cmds
// by the dispatcher once the handler returns.
type hostAdapter struct {
	m        *Model
	registry *command.Registry
	prompt   string
	quit     bool
	compact  bool
}

func (h *hostAdapter) Message(text string, sev command.Severity) {
	switch sev {
	case command.Warn:
		h.m.emit(block{kind: blockNotice, body: text, severity: noticeWarn})
	case command.Error:
		h.m.emit(block{kind: blockNotice, body: text, severity: noticeError})
	default:
		h.m.emit(block{kind: blockStatus, body: text})
	}
}

func (h *hostAdapter) SendUserMessage(text string) bool {
	if h.m.live.Active() || h.m.compacting {
		return false
	}
	h.prompt = text
	return true
}

func (h *hostAdapter) Mode() string {
	if checker := h.m.checker(); checker != nil {
		return string(checker.Mode())
	}
	return ""
}

func (h *hostAdapter) SetMode(mode string) error {
	checker := h.m.checker()
	if checker == nil {
		return errors.New("permissions are unavailable")
	}
	m := permissions.Mode(strings.ToLower(mode))
	switch m {
	case permissions.ModeDefault, permissions.ModeAcceptEdits, permissions.ModeBypass:
		checker.SetMode(m)
		return nil
	default:
		return fmt.Errorf("unknown permission mode %q", mode)
	}
}

func (h *hostAdapter) TokenUsage() command.TokenUsage {
	if h.m.opts.Compactor == nil {
		return command.TokenUsage{Level: compaction.LevelOK.String()}
	}
	return command.TokenUsage{
		Used:   h.m.opts.Compactor.UsedTokens(),
		Window: h.m.opts.Compactor.ContextWindow(),
		Level:  h.m.opts.Compactor.Level().String(),
	}
}

func (h *hostAdapter) Status() command.Status {
	p := h.m.opts.Provider
	s := command.Status{
		Version:  h.m.opts.Version,
		Model:    p.Model,
		Provider: p.Name,
		Protocol: string(p.Protocol),
		WorkDir:  h.m.opts.WorkDir,
		Mode:     h.Mode(),
		MCP:      h.m.mcpStatus,
	}
	if h.m.session != nil {
		s.SessionID = h.m.session.ID()
	}
	if h.m.opts.Conv != nil {
		s.Messages = h.m.opts.Conv.Len()
	}
	return s
}

func (h *hostAdapter) Commands() []*command.Command {
	return h.registry.All()
}

func (h *hostAdapter) Sessions() ([]command.SessionInfo, error) {
	infos, err := session.List(h.m.opts.WorkDir)
	if err != nil {
		return nil, err
	}
	current := ""
	if h.m.session != nil {
		current = h.m.session.ID()
	}
	out := make([]command.SessionInfo, 0, len(infos))
	for _, info := range infos {
		out = append(out, command.SessionInfo{
			ID:       info.ID,
			Title:    info.Title,
			Messages: info.Messages,
			ModTime:  info.ModTime,
			Current:  info.ID == current,
		})
	}
	return out, nil
}

func (h *hostAdapter) CurrentSession() command.SessionInfo {
	s := command.SessionInfo{}
	if h.m.session != nil {
		s.ID = h.m.session.ID()
	}
	if h.m.opts.Conv != nil {
		s.Messages = h.m.opts.Conv.Len()
	}
	return s
}

func (h *hostAdapter) NewSession() error {
	h.m.clearSession()
	return nil
}

func (h *hostAdapter) Compact() { h.compact = true }

func (h *hostAdapter) Quit() { h.quit = true }
