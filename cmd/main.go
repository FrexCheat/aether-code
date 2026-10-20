package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/command"
	"github.com/FrexCheat/aether-code/internal/compaction"
	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/mcp"
	"github.com/FrexCheat/aether-code/internal/permissions"
	"github.com/FrexCheat/aether-code/internal/prompt"
	"github.com/FrexCheat/aether-code/internal/session"
	"github.com/FrexCheat/aether-code/internal/skills"
	"github.com/FrexCheat/aether-code/internal/tools"
	"github.com/FrexCheat/aether-code/internal/tui"
	"github.com/FrexCheat/aether-code/internal/version"
)

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fail(err)
	}

	opts, err := parseFlags(os.Args[1:], os.Stderr)
	if err != nil {
		os.Exit(2)
	}
	if opts.list {
		// Pure filesystem work, deliberately ahead of the config load so a
		// listing never depends on a working provider.
		session.Cleanup(session.MaxAge)
		infos, err := session.List(cwd)
		if err != nil {
			fail(err)
		}
		printList(os.Stdout, cwd, infos)
		return
	}

	cfg, err := config.Load(cwd)
	if err != nil {
		fail(err)
	}
	active := cfg.Active()

	// A command name or alias claimed twice is a programming error; New panics
	// on it here, at startup, rather than at whatever later keystroke hits it.
	commands := command.New(command.Builtins()...)

	env := prompt.DetectEnvironment(cwd)
	catalog := skills.Load(cwd)
	state := skills.NewState(catalog)

	// The system prompt is assembled once here and never changes again: it
	// carries the available-skills listing, and changing it mid-session would
	// invalidate the cached prefix along with every message behind it.
	systemPrompt := prompt.BuildSystemPrompt(env, catalog.Listing())

	client, err := llm.NewClient(active, nil, systemPrompt)
	if err != nil {
		fail(err)
	}

	fileStates := tools.NewFileStateCache()
	registry := tools.NewBuiltinRegistry(cwd, fileStates)

	mcpMgr := mcp.NewManager(cfg.MCPServers, cfg.MCPServerWarnings)

	checker, err := permissions.NewChecker(cwd)
	if err != nil {
		fail(err)
	}

	conv := conversation.NewManager()

	session.Cleanup(session.MaxAge)
	sess, notice, err := startSession(conv, cwd, opts)
	if err != nil {
		fail(err)
	}
	defer sess.Close()

	if warnings := checker.RuleWarnings(); len(warnings) > 0 {
		// A rule file that did not parse silently contributes nothing —
		// denies included — so the user hears about it here. The same goes for
		// a Skill that was skipped: silence would leave them believing they
		// have one.
		notice = joinNotice(notice, warnings)
	}
	notice = joinNotice(notice, catalog.Notes())

	// The agent reads its own spill files without a boundary prompt, and so does
	// the user-level Skill directory: a bundle's reference documents are part of
	// the program the user installed, not something to be asked about on every
	// read.
	checker.AddAllowedRoot(sess.SpillDir())
	if root := config.DataRoot(); root != "" {
		checker.AddAllowedRoot(filepath.Join(root, "skills"))
	}

	compactor := compaction.NewManager(compaction.ManagerParams{
		Client:        client,
		Conv:          conv,
		Schemas:       registry.Schemas,
		SystemPrompt:  systemPrompt,
		ContextWindow: active.ContextWindow,
		MaxOutput:     active.MaxOutputTokens,
		SpillDir:      sess.SpillDir(),
		// Instructions the session loaded explicitly come back after a
		// compaction: the summary replaced them without restating them, and the
		// model cannot tell that it was following a procedure.
		Recovery: func() string { return state.RecoveryBlocks() },
	})

	ag := agent.New(client, registry, checker, mcpMgr.DeferredNames, compactor)

	registry.Register(skills.NewTool(state))

	// A resume does nothing Skill-related: the blocks are in the replayed
	// history as they were delivered, and the ledger starts empty.

	if err := tui.Run(tui.Options{
		Agent:         ag,
		Conv:          conv,
		MCP:           mcpMgr,
		Compactor:     compactor,
		Commands:      commands,
		Provider:      active,
		Version:       version.Version,
		WorkDir:       cwd,
		Session:       sess,
		Skills:        state,
		StartupNotice: notice,
	}); err != nil {
		fail(err)
	}
}

// joinNotice appends a group of notes to the startup notice, one per line,
// skipping an empty group so the notice never opens on a blank line.
func joinNotice(notice string, notes []string) string {
	if len(notes) == 0 {
		return notice
	}
	if notice != "" {
		notice += "\n"
	}
	return notice + strings.Join(notes, "\n")
}

// startSession opens the session the flags asked for and returns the notice
// shown above the banner: a fresh session silently, a resume with what it
// recovered, and a --continue that found nothing with a hint instead of an
// error.
func startSession(conv *conversation.Manager, cwd string, opts flags) (*session.Session, string, error) {
	switch {
	case opts.resumeID != "":
		sess, info, err := session.Resume(conv, opts.resumeID)
		if err != nil {
			return nil, "", err
		}
		return sess, resumeNotice(info, cwd), nil
	case opts.cont:
		sess, info, err := session.Continue(conv, cwd)
		if errors.Is(err, session.ErrNoSession) {
			sess, err := session.New(conv, cwd)
			if err != nil {
				return nil, "", err
			}
			return sess, "No previous session for this directory — starting a new one.", nil
		}
		if err != nil {
			return nil, "", err
		}
		return sess, resumeNotice(info, cwd), nil
	default:
		sess, err := session.New(conv, cwd)
		if err != nil {
			return nil, "", err
		}
		return sess, "", nil
	}
}

func resumeNotice(info session.Info, cwd string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Resumed session %s — %d messages", info.ID, info.Messages)
	if info.Skipped > 0 {
		fmt.Fprintf(&b, ", %d unreadable record(s) skipped", info.Skipped)
	}
	if info.Dropped > 0 {
		fmt.Fprintf(&b, ", %d interrupted tool turn(s) dropped", info.Dropped)
	}
	if info.Cwd != "" && info.Cwd != cwd {
		fmt.Fprintf(&b, " — recorded in %s", info.Cwd)
	}
	return b.String()
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "aethercode:", err)
	os.Exit(1)
}
