package skills

import (
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestMarker(t *testing.T) {
	tests := []struct {
		name, args string
		want       string
		line       string
		wantOK     bool
		wantName   string
		wantArgs   string
	}{
		{name: "commit", want: "[aether:skill commit]", line: "[aether:skill commit]"},
		{name: "commit", args: "fix parser", want: "[aether:skill commit] fix parser"},
		{name: "review", args: "src/a b.go", want: "[aether:skill review] src/a b.go"},
		{name: "报告", args: "本周", want: "[aether:skill 报告] 本周"},
	}
	for _, tt := range tests {
		line := Marker(tt.name, tt.args)
		if line != tt.want {
			t.Errorf("Marker(%q, %q) = %q, want %q", tt.name, tt.args, line, tt.want)
		}
		name, args, ok := ParseMarker(line)
		if !ok || name != tt.name || args != tt.args {
			t.Errorf("ParseMarker(%q) = (%q, %q, %v), want (%q, %q, true)", line, name, args, ok, tt.name, tt.args)
		}
	}

	notMarkers := []string{
		"",
		"hello",
		"[aether:skill] no name",
		"[aether:skill ] empty",
		"a [aether:skill commit] mid-line",
		"[aether:skills commit] wrong tag",
	}
	for _, line := range notMarkers {
		if _, _, ok := ParseMarker(line); ok {
			t.Errorf("ParseMarker(%q) matched, want no match", line)
		}
	}

	if !IsBlock(Marker("commit", "x") + "\nbody") {
		t.Error("a block's first line is a marker")
	}
	if IsBlock("a question\n" + Marker("commit", "")) {
		t.Error("a marker further down is not what makes a message a block")
	}
}

func TestStateActivation(t *testing.T) {
	_, project, cwd := tiers(t)
	writeSkill(t, project, "a.md", "---\ndescription: a\n---\n正文 a\n")
	writeSkill(t, project, "b.md", "---\ndescription: b\n---\n正文 b\n")
	state := NewState(Load(cwd))

	if _, err := state.Deliver(Activation{Name: "nope"}); err == nil {
		t.Error("delivering an unknown name must fail")
	}
	if _, err := state.Deliver(Activation{Name: "a", Args: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Deliver(Activation{Name: "b"}); err != nil {
		t.Fatal(err)
	}
	// The same name keeps its place and takes the new arguments.
	if _, err := state.Deliver(Activation{Name: "a", Args: "second"}); err != nil {
		t.Fatal(err)
	}
	active := state.Active()
	want := []Activation{{Name: "a", Args: "second"}, {Name: "b"}}
	if len(active) != len(want) || active[0] != want[0] || active[1] != want[1] {
		t.Errorf("Active() = %+v, want %+v", active, want)
	}
	if cleared := state.Clear(); cleared != 2 {
		t.Errorf("Clear() = %d, want 2", cleared)
	}
	if len(state.Active()) != 0 {
		t.Error("Clear() left activations behind")
	}
}

func TestStateConcurrentAccess(t *testing.T) {
	_, project, cwd := tiers(t)
	writeSkill(t, project, "a.md", "---\ndescription: a\n---\n正文\n")
	writeSkill(t, project, "b.md", "---\ndescription: b\n---\n正文\n")
	state := NewState(Load(cwd))

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 20 {
				_, _ = state.Deliver(Activation{Name: "a", Args: "x"})
			}
		}()
		go func(i int) {
			defer wg.Done()
			for range 20 {
				_, _ = state.Deliver(Activation{Name: "b", Args: "x"})
				_ = state.Active()
				_ = state.RecoveryBlocks()
			}
		}(i)
	}
	wg.Wait()
	if got := len(state.Active()); got != 2 {
		t.Errorf("Active() has %d entries, want 2", got)
	}
}

func TestBlock(t *testing.T) {
	_, project, cwd := tiers(t)
	writeSkill(t, project, "plan.md",
		"---\ndescription: 计划\n---\n先看 $ARGUMENTS，再动手。\n")
	writeSkill(t, project, "pack/SKILL.md",
		"---\ndescription: 能力包\n---\n用 templates/brief.md。\n")
	writeSkill(t, project, "pack/templates/brief.md", "# 简报模板\n")
	writeSkill(t, project, "pack/reference/metrics.md", "# 指标\n")
	writeSkill(t, project, "plain.md", "---\ndescription: 无占位符\n---\n照做。\n")
	writeSkill(t, project, "blank.md", "---\ndescription: 空\n---\n")
	state := NewState(Load(cwd))

	tests := []struct {
		name   string
		args   string
		wants  []string
		absent []string
	}{
		{
			name:  "plan",
			args:  "本周目标",
			wants: []string{"[aether:skill plan] 本周目标", blockLead, "先看 本周目标，再动手。"},
		},
		{
			name: "pack",
			wants: []string{
				"[aether:skill pack]",
				"资源目录：" + filepath.Join(project, "pack"),
				"包内文件：reference/metrics.md, templates/brief.md",
			},
			absent: []string{"SKILL.md"},
		},
		{
			// No placeholder: the arguments are appended rather than dropped.
			name:  "plain",
			args:  "额外要求",
			wants: []string{"照做。\n\n额外要求"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block, err := state.Deliver(Activation{Name: tt.name, Args: tt.args})
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tt.wants {
				if !strings.Contains(block, want) {
					t.Errorf("block misses %q:\n%s", want, block)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(block, absent) {
					t.Errorf("block must not carry %q:\n%s", absent, block)
				}
			}
		})
	}

	if _, err := (&State{cat: Load(t.TempDir())}).Deliver(Activation{Name: "gone"}); err == nil {
		t.Error("delivering a name the catalog no longer has must fail")
	}
	// An unreadable body is not an activation: nothing may be recorded for
	// instructions that were never delivered.
	if _, err := state.Deliver(Activation{Name: "blank"}); err == nil {
		t.Error("delivering an empty body must fail")
	}
	if active := state.Active(); slices.ContainsFunc(active, func(a Activation) bool { return a.Name == "blank" }) {
		t.Error("a failed delivery was recorded as active")
	}
}

func TestBlockCaps(t *testing.T) {
	_, project, cwd := tiers(t)
	body := strings.Repeat("内", maxBlockBytes) // three bytes per rune: well past the cap
	writeSkill(t, project, "big.md", "---\ndescription: big\n---\n"+body+"\n")
	state := NewState(Load(cwd))

	block, err := state.Deliver(Activation{Name: "big"})
	if err != nil {
		t.Fatal(err)
	}
	if len(block) > maxBlockBytes+200 {
		t.Errorf("block is %d bytes, want it capped near %d", len(block), maxBlockBytes)
	}
	if !strings.Contains(block, "… (truncated — read "+filepath.Join(project, "big.md")+" for the rest)") {
		t.Errorf("a capped block must say where the rest is:\n%s", block[len(block)-200:])
	}
}

func TestRecoveryBlocks(t *testing.T) {
	_, project, cwd := tiers(t)
	writeSkill(t, project, "first.md", "---\ndescription: first\n---\n第一套流程。\n")
	writeSkill(t, project, "second.md", "---\ndescription: second\n---\n第二套流程。\n")
	writeSkill(t, project, "huge1.md", "---\ndescription: 甲\n---\n"+strings.Repeat("甲", maxBlockBytes)+"\n")
	writeSkill(t, project, "huge2.md", "---\ndescription: 乙\n---\n"+strings.Repeat("乙", maxBlockBytes)+"\n")
	state := NewState(Load(cwd))

	if got := state.RecoveryBlocks(); got != "" {
		t.Errorf("nothing active must recover as nothing, got %q", got)
	}
	for _, name := range []string{"first", "second"} {
		if _, err := state.Deliver(Activation{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	recovery := state.RecoveryBlocks()
	if !strings.Contains(recovery, recoveryLead) {
		t.Errorf("recovery misses its lead-in:\n%s", recovery)
	}
	first, second := strings.Index(recovery, "第一套流程。"), strings.Index(recovery, "第二套流程。")
	if first < 0 || second < 0 || second > first {
		t.Errorf("the newest activation must come first (second at %d, first at %d)", second, first)
	}

	// Two capped blocks fill the budget, so the older one is named rather than
	// dropped in silence.
	for _, name := range []string{"huge1", "huge2"} {
		if _, err := state.Deliver(Activation{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	over := state.RecoveryBlocks()
	if len(over) > maxRecoveryBytes+1024 {
		t.Errorf("recovery is %d bytes, want it capped near %d", len(over), maxRecoveryBytes)
	}
	if !strings.Contains(over, "乙") || strings.Contains(over, "甲甲") {
		t.Error("the newest activation is the one that must survive the budget")
	}
	if !strings.Contains(over, "1 条较早激活的指令因篇幅未列出") {
		t.Errorf("recovery must say what it left out:\n%s", over[len(over)-200:])
	}
}
