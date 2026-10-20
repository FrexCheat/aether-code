package command

import "time"

// Severity selects how a message is shown: info as a plain status block, warn
// and error as notices.
type Severity int

const (
	Info Severity = iota
	Warn
	Error
)

// TokenUsage is the context budget readout.
type TokenUsage struct {
	Used   int
	Window int
	Level  string // the level word from compaction.Level.String(): ok / low / critical
}

// Status is a snapshot of the running window for /status. Empty fields are
// dropped by the presenter.
type Status struct {
	Version   string
	Model     string
	Provider  string
	Protocol  string
	WorkDir   string
	SessionID string
	Messages  int
	Mode      string
	MCP       string
	Skills    string
}

// SkillInfo is one loaded Skill, for /skills.
type SkillInfo struct {
	Name        string
	Description string
	Source      string
	Active      bool
	// Slash reports whether the Skill is reachable as /name: a built-in
	// command keeps its spelling when a Skill asks for the same one.
	Slash bool
}

// SkillReport is the whole /skills readout: what is loaded, and what the scan
// had to skip.
type SkillReport struct {
	Skills []SkillInfo
	Notes  []string
}

// SessionInfo is one archive in the /session listing.
type SessionInfo struct {
	ID       string
	Title    string
	Messages int
	ModTime  time.Time
	Current  bool
}

// Host is everything a command may do to the running window. It is deliberately
// narrow and free of any rendering type, so a command implementation never
// depends on the TUI. Effects that the window can only perform asynchronously
// (quitting, compacting, sending a message) are requested here and carried out
// by the dispatcher.
type Host interface {
	// Message prints text into the window.
	Message(text string, sev Severity)
	// SendUserMessage submits text exactly as if the user typed it and pressed
	// Enter. It reports false when a turn is already running.
	SendUserMessage(text string) bool
	// Mode reports the current permission mode.
	Mode() string
	// SetMode switches the permission mode.
	SetMode(mode string) error
	// TokenUsage reports the context budget.
	TokenUsage() TokenUsage
	// Status returns the /status snapshot.
	Status() Status
	// Commands lists the registered commands, for /help.
	Commands() []*Command
	// Sessions lists the saved sessions for the working directory.
	Sessions() ([]SessionInfo, error)
	// CurrentSession describes the active session.
	CurrentSession() SessionInfo
	// NewSession rotates to a fresh session archive.
	NewSession() error
	// Compact requests a compaction.
	Compact()
	// Skills reports the loaded Skills.
	Skills() SkillReport
	// ReloadSkills rescans the Skill directories and reports what it found.
	ReloadSkills()
	// ActivateSkill loads a Skill the user typed as a slash command into the
	// conversation.
	ActivateSkill(name, args string) error
	// Quit requests that the program exits.
	Quit()
}
