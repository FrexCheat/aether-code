// Package command defines the slash command system: what a command is, how the
// registry detects conflicts, how a line is parsed and how a command talks back
// to the running window. It depends on nothing else in the program — a command
// reaches the window only through Host, so implementations stay independent of
// the renderer and of the services behind it.
package command

// Kind is how a command executes.
type Kind int

const (
	// Local commands read state, print and quit; they leave the window and the
	// conversation as they were.
	Local Kind = iota
	// UI commands change the window or the session.
	UI
	// Prompt commands hand a preset prompt to the model.
	Prompt
)

// Command is one slash command: the spellings that select it, what it is for
// and how it runs.
type Command struct {
	Name        string   // primary spelling, lowercase, without the slash
	Aliases     []string // alternative spellings, matched case-insensitively
	Description string   // one line for /help and the completion menu
	Usage       string   // e.g. "/session [list]"
	ArgHint     string   // e.g. "[list]"
	Kind        Kind
	// Immediate marks a command that may run while a turn is streaming. Every
	// other command waits for the turn to end, like a plain message does.
	Immediate bool
	// Run is the handler; it is nil for Prompt commands, which carry a template
	// instead.
	Run func(h Host, args []string) error
	// Prompt is the preset template for a Prompt command. It may contain
	// $ARGUMENTS, which RenderPrompt substitutes.
	Prompt string
}
