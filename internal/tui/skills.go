package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/FrexCheat/aether-code/internal/command"
	"github.com/FrexCheat/aether-code/internal/skills"
)

// skillOpener is what a slash-activated Skill sends when the user gave
// no arguments: a turn needs something to start from, and instructions with
// nobody asking for anything are not a request.
const skillOpener = "按已激活的 Skill 指令执行。"

// skillRequest is a Skill a command asked for. The window carries it out after
// the handler returns, like every other effect a command can only request.
type skillRequest struct {
	name string
	args string
}

// skillCommands turns the loaded Skills into the dynamic slash layer. A name a
// built-in command already spells is dropped there rather than here, so the
// check sees the static layer alone.
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

// shadowNote explains a Skill that has no slash spelling of its own.
func shadowNote(name string) string {
	return fmt.Sprintf("Skill %q is not reachable as /%s: a built-in command owns that name.", name, name)
}

// applySkillCommands rebuilds the dynamic layer and returns the notes for what
// it could not put there.
func (m *Model) applySkillCommands() []string {
	var notes []string
	for _, name := range m.opts.Commands.SetDynamic(skillCommands(m.opts.Skills.Catalog())) {
		notes = append(notes, shadowNote(name))
	}
	return notes
}

// syncSkills rescans the Skill directories ahead of a submitted line, so a
// Skill written while the window was open is usable on the very next one. force
// is the /skills reload switch: it reports even when nothing moved.
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
	// The listing in the system prompt is frozen, so a Skill that appeared or
	// vanished mid-session is announced in the conversation instead.
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

// activateSkill delivers a Skill from the slash path: the arguments are
// the turn's opener, the rendered instructions follow as a second user message,
// and the instructions themselves are not printed — they are what the model is
// told, not what it found. The status line says the activation happened.
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

// activationLine is the user-visible confirmation of an activation.
func activationLine(a skills.Activation) string {
	if a.Args == "" {
		return "Skill " + a.Name + " activated."
	}
	return "Skill " + a.Name + " activated · " + a.Args
}

// skillChangeLine is the one-line readout of a rescan.
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
