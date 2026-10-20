package prompt

import (
	"strings"
	"testing"
)

func testEnvironment(t *testing.T) Environment {
	t.Helper()
	return Environment{
		WorkDir: t.TempDir(),
		OS:      "linux",
		Arch:    "amd64",
		Shell:   "bash",
		Date:    "2026-10-12",
	}
}

func TestSystemPromptSkillListing(t *testing.T) {
	env := testEnvironment(t)
	without := BuildSystemPrompt(env, "")
	if strings.Contains(without, "可用 Skill") {
		t.Error("an empty listing must leave no section behind")
	}

	listing := "- commit — 把当前改动整理成一次提交\n- review — 审查未提交的改动"
	with := BuildSystemPrompt(env, listing)

	start := strings.Index(with, "## 可用 Skill")
	if start < 0 {
		t.Fatal("the listing section is missing")
	}
	if !strings.Contains(with[start:], listing) {
		t.Error("the bullets must ride the section")
	}
	// It belongs with the other "what else can you call" section, ahead of the
	// writing-style ones.
	mcp := strings.Index(with, "## 外部 MCP 工具")
	style := strings.Index(with, "## 语气与风格")
	if !(0 <= mcp && mcp < start && start < style) {
		t.Errorf("the section sits at %d, want it between the MCP section (%d) and the style one (%d)", start, mcp, style)
	}
	if stripped := with[:start] + with[style:]; stripped != without {
		t.Error("the listing must add a section and change nothing else")
	}
}

func TestSystemPromptDropsEmptySections(t *testing.T) {
	// A directory outside a repository has no AETHER.md chain, so the section
	// costs nothing rather than arriving empty.
	prompt := BuildSystemPrompt(testEnvironment(t), "")
	if strings.Contains(prompt, "## 项目指令") {
		t.Error("an empty instruction chain must drop out entirely")
	}
	for _, want := range []string{"## 环境", "## 使用你的工具", "## 语气与风格"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt misses %q", want)
		}
	}
}
