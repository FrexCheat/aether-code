// Package command defines slash commands and the narrow host they can affect.
package command

type Kind int

const (
	Local Kind = iota
	UI
	Prompt
	Skill
)

type Command struct {
	Name        string   // primary spelling, lowercase, without the slash
	Aliases     []string // alternative spellings, matched case-insensitively
	Description string   // one line for /help and the completion menu
	Usage       string   // e.g. "/session [list]"
	ArgHint     string   // e.g. "[list]"
	Kind        Kind
	Immediate   bool
	// Run is nil for Prompt and Skill commands; the dispatcher handles those.
	Run    func(h Host, args []string) error
	Prompt string
}
