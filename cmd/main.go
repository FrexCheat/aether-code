package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/compaction"
	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/mcp"
	"github.com/FrexCheat/aether-code/internal/permissions"
	"github.com/FrexCheat/aether-code/internal/prompt"
	"github.com/FrexCheat/aether-code/internal/session"
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

	systemPrompt := prompt.BuildSystemPrompt(prompt.DetectEnvironment(cwd))

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
		// denies included — so the user hears about it here.
		if notice != "" {
			notice += "\n"
		}
		notice += strings.Join(warnings, "\n")
	}

	// The agent reads its own spill files without a boundary prompt; the
	// user-level settings file (it holds API keys) stays out.
	checker.AddAllowedRoot(sess.SpillDir())

	compactor := compaction.NewManager(compaction.ManagerParams{
		Client:        client,
		Conv:          conv,
		Schemas:       registry.Schemas,
		SystemPrompt:  systemPrompt,
		ContextWindow: active.ContextWindow,
		MaxOutput:     active.MaxOutputTokens,
		SpillDir:      sess.SpillDir(),
	})

	ag := agent.New(client, registry, checker, mcpMgr.DeferredNames, compactor)

	if err := tui.Run(tui.Options{
		Agent:         ag,
		Conv:          conv,
		MCP:           mcpMgr,
		Compactor:     compactor,
		Provider:      active,
		Version:       version.Version,
		WorkDir:       cwd,
		Session:       sess,
		StartupNotice: notice,
	}); err != nil {
		fail(err)
	}
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
