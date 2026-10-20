package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/FrexCheat/aether-code/internal/command"
	"github.com/FrexCheat/aether-code/internal/skills"
)

const skillOpener = "按已激活的 Skill 指令执行。"

type skillRequest struct {
	name string
	args string
}

func skillCommands(catalog *skills.Catalog) []*command.Command {
	loaded := catalog.List()
	commands := make([]*command.Command, 0, len(loaded))
	for _, skill := range loaded {
		commands = append(commands, &command.Command{
			Name:        skill.Name,
			Description: skill.Description,
			Usage:       "/" + skill.Name + " [args]",
			ArgHint:     "[args]",
			Kind:        command.Skill,
		})
	}
	return commands
}

func shadowNote(name string) string {
	return fmt.Sprintf("Skill %q is not reachable as /%s: a built-in command owns that name.", name, name)
}

func (m *Model) applySkillCommands() []string {
	var notes []string
	for _, name := range m.opts.Commands.SetDynamic(skillCommands(m.opts.Skills.Catalog())) {
		notes = append(notes, shadowNote(name))
	}
	return notes
}

func (m *Model) syncSkills(force bool) {
	if m.opts.Skills == nil {
		return
	}
	changed, notes := m.opts.Skills.Refresh()
	if !changed && !force {
		return
	}
	notes = append(notes, m.applySkillCommands()...)
	for _, note := range notes {
		m.transcript.Emit(block{kind: blockNotice, body: note, severity: noticeWarn})
	}

	change := m.opts.Skills.Pending()
	m.transcript.Emit(block{kind: blockStatus, body: skillChangeLine(change)})
	if notice := change.Notice(); notice != "" {
		if err := m.opts.Conv.AddUserMessage(notice); err != nil {
			m.transcript.Emit(block{
				kind:     blockNotice,
				body:     "Cannot record the Skill change: " + err.Error(),
				severity: noticeError,
			})
		}
	}
}

func (m *Model) activateSkill(request skillRequest) (Model, tea.Cmd) {
	activation := skills.Activation{Name: request.name, Args: request.args}
	body, err := m.opts.Skills.Deliver(activation)
	if err != nil {
		m.transcript.Emit(block{kind: blockNotice, body: err.Error(), severity: noticeError})
		return *m, nil
	}
	opener := request.args
	if opener == "" {
		opener = skillOpener
	}
	if err := m.opts.Conv.AddUserMessage(opener); err != nil {
		m.transcript.Emit(block{kind: blockNotice, body: "Cannot send message: " + err.Error(), severity: noticeError})
		return *m, nil
	}
	if err := m.opts.Conv.AddUserMessage(body); err != nil {
		m.transcript.Emit(block{
			kind:     blockNotice,
			body:     "Cannot record the Skill instructions: " + err.Error(),
			severity: noticeError,
		})
		return *m, nil
	}
	m.transcript.Emit(block{kind: blockUser, body: opener})
	m.transcript.Emit(block{kind: blockStatus, body: activationLine(activation)})
	return m.startTurn()
}

func activationLine(a skills.Activation) string {
	if a.Args == "" {
		return "Skill " + a.Name + " activated."
	}
	return "Skill " + a.Name + " activated · " + a.Args
}

func skillChangeLine(c skills.Change) string {
	switch {
	case len(c.Added) == 0 && len(c.Removed) == 0:
		return "Skill catalog unchanged."
	case len(c.Removed) == 0:
		return fmt.Sprintf("Skill catalog updated: %d added.", len(c.Added))
	case len(c.Added) == 0:
		return fmt.Sprintf("Skill catalog updated: %d removed.", len(c.Removed))
	default:
		return fmt.Sprintf("Skill catalog updated: %d added, %d removed.", len(c.Added), len(c.Removed))
	}
}
