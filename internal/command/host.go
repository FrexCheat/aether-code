package command

import "time"

type Severity int

const (
	Info Severity = iota
	Warn
	Error
)

type TokenUsage struct {
	Used   int
	Window int
	Level  string // the level word from compaction.Level.String(): ok / low / critical
}

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

type SkillInfo struct {
	Name        string
	Description string
	Source      string
	Active      bool
	Slash       bool
}

type SkillReport struct {
	Skills []SkillInfo
	Notes  []string
}

type SessionInfo struct {
	ID       string
	Title    string
	Messages int
	ModTime  time.Time
	Current  bool
}

// Host keeps command implementations independent of the TUI.
type Host interface {
	Message(text string, sev Severity)
	SendUserMessage(text string) bool
	Mode() string
	SetMode(mode string) error
	TokenUsage() TokenUsage
	Status() Status
	Commands() []*Command
	Sessions() ([]SessionInfo, error)
	CurrentSession() SessionInfo
	NewSession() error
	Compact()
	Skills() SkillReport
	ReloadSkills()
	ActivateSkill(name, args string) error
	Quit()
}
