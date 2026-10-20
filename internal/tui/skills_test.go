package tui

import (
	"context"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FrexCheat/aether-code/internal/agent"
	"github.com/FrexCheat/aether-code/internal/command"
	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/skills"
	"github.com/FrexCheat/aether-code/internal/tools"
)

// silentClient ends every stream at once: a dispatch test cares about what
// reached the history before the model was asked anything.
type silentClient struct{}

func (silentClient) Stream(context.Context, llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		yield(llm.StreamEnd{}, nil)
	}
}

// skillModel builds a window over a project tier holding two Skills.
func skillModel(t *testing.T) (Model, *skills.State) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	writeSkillFile(t, filepath.Join(cwd, ".aether", "skills", "plan.md"),
		"---\ndescription: 计划\n---\n先看 $ARGUMENTS，再动手。\n")
	writeSkillFile(t, filepath.Join(cwd, ".aether", "skills", "audit.md"),
		"---\ndescription: 审查\n---\n审查未提交的改动。\n")

	state := skills.NewState(skills.Load(cwd))
	commands := command.New(command.Builtins()...)
	m := newModel(Options{
		Agent:    agent.New(silentClient{}, tools.NewRegistry(), nil, nil, nil),
		Conv:     conversation.NewManager(),
		Commands: commands,
		Skills:   state,
		Version:  "9.9.9",
		WorkDir:  cwd,
		Provider: config.Provider{Name: "prov", Model: "mod", Protocol: config.ProtocolAnthropic},
	})
	return m, state
}

func writeSkillFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func blockKinds(blocks []block) []blockKind {
	kinds := make([]blockKind, len(blocks))
	for i, b := range blocks {
		kinds[i] = b.kind
	}
	return kinds
}

// A delivered block in the history was the host's doing, not the user's: it
// reads as a notice, and the instructions are not reprinted on every resume.
func TestHistoryBlocksSkillDelivery(t *testing.T) {
	delivered := skills.Marker("plan", "本周目标") + "\n以下是由系统按请求加载的能力包指令…\n\n先看 本周目标，再动手。"
	msgs := []conversation.Message{
		{Role: conversation.RoleUser, Content: delivered},
		{Role: conversation.RoleUser, Content: "帮我看看这周的进度"},
	}
	blocks := historyBlocks(msgs, nil, nil)
	if got := blockKinds(blocks); len(got) != 2 || got[0] != blockNotice || got[1] != blockUser {
		t.Fatalf("kinds = %v, want a notice then the user's message", got)
	}
	if blocks[0].body != "Skill plan activated · 本周目标" {
		t.Errorf("notice = %q", blocks[0].body)
	}
	if strings.Contains(blocks[0].body, "以下是由系统") {
		t.Error("the instructions must not be reprinted in the resumed view")
	}
	if blocks[1].body != "帮我看看这周的进度" {
		t.Errorf("the user's own message changed: %q", blocks[1].body)
	}
}

// The other envelope is untouchable: what arrived as a tool result stays a tool
// result, instructions and all.
func TestHistoryBlocksKeepsToolDeliveredInstructions(t *testing.T) {
	delivered := skills.Marker("plan", "") + "\n先看 本周目标。"
	msgs := []conversation.Message{
		{Role: conversation.RoleAssistant, ToolUses: []conversation.ToolUseBlock{
			{ID: "t1", Name: "Skill", Args: map[string]any{"name": "plan"}},
		}},
		{Role: conversation.RoleUser, ToolResults: []conversation.ToolResultBlock{
			{ToolUseID: "t1", Content: delivered},
		}},
	}
	blocks := historyBlocks(msgs, nil, nil)
	if len(blocks) != 1 || blocks[0].kind != blockTool {
		t.Fatalf("kinds = %v, want one tool block", blockKinds(blocks))
	}
	if blocks[0].body != delivered {
		t.Error("a tool result keeps its content, marker and all")
	}
}

// /plan <args> opens a turn with the user's words, delivers the instructions
// behind them, and says so.
func TestDispatchSkill(t *testing.T) {
	m, state := skillModel(t)
	m.composer.SetValue("/plan 本周目标")
	m, cmd := m.handleEnter()
	if cmd == nil {
		t.Fatal("an activation must start a turn")
	}

	msgs := m.opts.Conv.Messages()
	if len(msgs) != 2 {
		t.Fatalf("history grew by %d messages, want the request and the instructions", len(msgs))
	}
	if msgs[0].Content != "本周目标" {
		t.Errorf("the opener = %q, want the arguments", msgs[0].Content)
	}
	if !strings.HasPrefix(msgs[1].Content, "[aether:skill plan] 本周目标") {
		t.Errorf("the delivered block = %q", firstLineOf(msgs[1].Content))
	}
	if !strings.Contains(msgs[1].Content, "先看 本周目标，再动手。") {
		t.Error("the instructions must carry the expanded arguments")
	}
	if active := state.Active(); len(active) != 1 || active[0].Name != "plan" {
		t.Errorf("Active() = %+v, want the plan activation", active)
	}

	if got := blockKinds(m.transcript.blocks); len(got) != 2 || got[0] != blockUser || got[1] != blockStatus {
		t.Errorf("kinds = %v, want the request then a status line", got)
	}
	if body := m.transcript.blocks[1].body; body != "Skill plan activated · 本周目标" {
		t.Errorf("status = %q", body)
	}

	// /skills then shows what is loaded and which one is live. The turn above
	// is still open, and the input gate is not what this test is about.
	if m.turn != nil {
		m.turn.Close()
		m.turn = nil
	}
	m.live.Finish()
	m.composer.SetValue("/skills")
	m, _ = m.handleEnter()
	body := m.transcript.blocks[len(m.transcript.blocks)-1].body
	for _, want := range []string{"* plan", "计划", "[project]", "  audit", "审查"} {
		if !strings.Contains(body, want) {
			t.Errorf("/skills misses %q:\n%s", want, body)
		}
	}
}

func firstLineOf(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

// A Skill may be called anything, including a name a built-in already spells:
// the built-in keeps the spelling, the Skill keeps everything else, and the
// window says which is which rather than failing at startup.
func TestSkillNamedLikeABuiltin(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	writeSkillFile(t, filepath.Join(cwd, ".aether", "skills", "help.md"),
		"---\ndescription: 冒充内置命令\n---\n正文\n")
	state := skills.NewState(skills.Load(cwd))
	commands := command.New(command.Builtins()...)

	m := newModel(Options{
		Conv:     conversation.NewManager(),
		Commands: commands,
		Skills:   state,
		Version:  "9.9.9",
		WorkDir:  cwd,
		Provider: config.Provider{Name: "prov", Model: "mod", Protocol: config.ProtocolAnthropic},
	})

	if cmd := commands.Lookup("help"); cmd == nil || cmd.Kind != command.Local {
		t.Fatalf("Lookup(help) = %+v, want the built-in", cmd)
	}
	var notices []string
	for _, b := range m.transcript.blocks {
		notices = append(notices, b.body)
	}
	if !strings.Contains(strings.Join(notices, "\n"), "not reachable as /help") {
		t.Errorf("the shadowed name must be reported at startup: %q", notices)
	}

	m.composer.SetValue("/skills")
	m, _ = m.handleEnter()
	body := m.transcript.blocks[len(m.transcript.blocks)-1].body
	if !strings.Contains(body, "help") || !strings.Contains(body, "not reachable as /help") {
		t.Errorf("/skills misses the shadowed Skill:\n%s", body)
	}
}
