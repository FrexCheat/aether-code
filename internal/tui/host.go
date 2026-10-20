package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/FrexCheat/aether-code/internal/command"
	"github.com/FrexCheat/aether-code/internal/compaction"
	"github.com/FrexCheat/aether-code/internal/permissions"
	"github.com/FrexCheat/aether-code/internal/session"
	"github.com/FrexCheat/aether-code/internal/skills"
)

// hostAdapter is the Model as a command.Host: the renderer-agnostic surface a
// command talks to. Effects the window can only perform asynchronously — a sent
// message, a compaction, a quit — are recorded here and turned into tea.Cmds
// by the dispatcher once the handler returns.
type hostAdapter struct {
	m          *Model
	registry   *command.Registry
	prompt     string
	activation *skillRequest
	quit       bool
	compact    bool
}

func (h *hostAdapter) Message(text string, sev command.Severity) {
	switch sev {
	case command.Warn:
		h.m.transcript.Emit(block{kind: blockNotice, body: text, severity: noticeWarn})
	case command.Error:
		h.m.transcript.Emit(block{kind: blockNotice, body: text, severity: noticeError})
	default:
		h.m.transcript.Emit(block{kind: blockStatus, body: text})
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
		Skills:   skillsStatus(h.m.opts.Skills),
	}
	if h.m.opts.Session != nil {
		s.SessionID = h.m.opts.Session.ID()
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
	if h.m.opts.Session != nil {
		current = h.m.opts.Session.ID()
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
	if h.m.opts.Session != nil {
		s.ID = h.m.opts.Session.ID()
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

// Skills reports the catalog with its activation ledger, in the order the
// window would list them.
func (h *hostAdapter) Skills() command.SkillReport {
	state := h.m.opts.Skills
	if state == nil {
		return command.SkillReport{}
	}
	active := make(map[string]bool)
	for _, a := range state.Active() {
		active[a.Name] = true
	}
	report := command.SkillReport{Notes: slices.Clone(state.Catalog().Notes())}
	for _, skill := range state.List() {
		found := h.registry.Lookup(skill.Name)
		slash := found != nil && found.Kind == command.Skill
		if found != nil && !slash {
			report.Notes = append(report.Notes, shadowNote(skill.Name))
		}
		report.Skills = append(report.Skills, command.SkillInfo{
			Name:        skill.Name,
			Description: skill.Description,
			Source:      string(skill.Source),
			Active:      active[skill.Name],
			Slash:       slash,
		})
	}
	return report
}

func (h *hostAdapter) ReloadSkills() { h.m.syncSkills(true) }

// ActivateSkill records the Skill a slash command asked for, the way
// SendUserMessage records a prompt: carrying it out is the dispatcher's job,
// once the handler has returned.
func (h *hostAdapter) ActivateSkill(name, args string) error {
	if h.m.live.Active() || h.m.compacting {
		return errors.New("a Skill cannot start while a turn is running")
	}
	state := h.m.opts.Skills
	if state == nil {
		return errors.New("skills are unavailable in this session")
	}
	if state.Get(name) == nil {
		return fmt.Errorf("unknown skill %q", name)
	}
	h.activation = &skillRequest{name: name, args: args}
	return nil
}

func (h *hostAdapter) Quit() { h.quit = true }

// skillsStatus is the /status line: what is active, and how much of it.
func skillsStatus(state *skills.State) string {
	if state == nil {
		return ""
	}
	active := state.Active()
	if len(active) == 0 {
		return "none"
	}
	names := make([]string, len(active))
	for i, a := range active {
		names[i] = a.Name
	}
	return fmt.Sprintf("%s · %d active", strings.Join(names, ", "), len(active))
}
