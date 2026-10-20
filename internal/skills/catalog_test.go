package skills

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// tiers lays out an empty user tier and a project directory, and points the
// process at them: the builtin tier is the only one left from the real machine.
func tiers(t *testing.T) (user, project, cwd string) {
	t.Helper()
	cwd = t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".aether", "skills"), filepath.Join(cwd, ".aether", "skills"), cwd
}

// writeSkill writes one definition into a tier, creating any parents.
func writeSkill(t *testing.T, root, rel, content string) string {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCatalogLoad(t *testing.T) {
	user, project, cwd := tiers(t)
	writeSkill(t, user, "commit.md", "---\nname: commit\ndescription: 用户层\n---\n用户正文\n")
	writeSkill(t, user, "dup1.md", "---\nname: dup\ndescription: 先到者\n---\n正文\n")
	writeSkill(t, user, "dup2.md", "---\nname: dup\ndescription: 后到者\n---\n正文\n")
	writeSkill(t, user, "broken.md", "---\nname: [oops\n---\n正文\n")
	writeSkill(t, user, "report/SKILL.md", "---\ndescription: 生成周报\n---\n报告正文\n")
	writeSkill(t, user, "report/templates/weekly.md", "# 模板\n")
	// Only SKILL.md is an entry, so a second definition inside the bundle is
	// not one — stage one never looks at it.
	writeSkill(t, user, "report/other.md", "---\nname: other\n---\n正文\n")
	if err := os.MkdirAll(filepath.Join(user, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, project, "commit.md", "---\nname: commit\ndescription: 项目层\n---\n项目正文\n")

	catalog := Load(cwd)

	tests := []struct {
		name        string
		wantSource  Source
		wantDesc    string
		wantDir     bool
		wantMissing bool
	}{
		{name: "commit", wantSource: SourceProject, wantDesc: "项目层"},
		{name: "report", wantSource: SourceUser, wantDesc: "生成周报", wantDir: true},
		{name: "dup", wantSource: SourceUser, wantDesc: "先到者"},
		{name: "test", wantSource: SourceBuiltin},
		{name: "other", wantMissing: true},
	}
	for _, tt := range tests {
		skill := catalog.Get(tt.name)
		if tt.wantMissing {
			if skill != nil {
				t.Errorf("%s must not be loaded: %s", tt.name, skill.Path)
			}
			continue
		}
		if skill == nil {
			t.Errorf("%s was not loaded", tt.name)
			continue
		}
		if skill.Source != tt.wantSource {
			t.Errorf("%s came from %s, want %s", tt.name, skill.Source, tt.wantSource)
		}
		if tt.wantDesc != "" && skill.Description != tt.wantDesc {
			t.Errorf("%s description = %q, want %q", tt.name, skill.Description, tt.wantDesc)
		}
		if got := skill.Dir != ""; got != tt.wantDir {
			t.Errorf("%s Dir set = %v, want %v", tt.name, got, tt.wantDir)
		}
	}

	var notes string
	for _, note := range catalog.Notes() {
		notes += note + "\n"
	}
	for _, want := range []string{"broken.md", "dup2.md", "empty"} {
		if !strings.Contains(notes, want) {
			t.Errorf("the load report misses %s:\n%s", want, notes)
		}
	}

	names := make([]string, 0, len(catalog.List()))
	for _, skill := range catalog.List() {
		names = append(names, skill.Name)
	}
	if !slices.IsSorted(names) {
		t.Errorf("List() is not in name order: %q", names)
	}
}

func TestCatalogListing(t *testing.T) {
	_, project, cwd := tiers(t)
	writeSkill(t, project, "report.md", "---\ndescription: 生成周报\n---\n正文不该出现\n")

	listing := Load(cwd).Listing()
	lines := strings.Split(listing, "\n")
	if len(lines) != 4 {
		t.Fatalf("listing has %d lines, want one per Skill:\n%s", len(lines), listing)
	}
	if !slices.IsSorted(lines) {
		t.Errorf("listing is not in name order:\n%s", listing)
	}
	for _, want := range []string{"- commit — ", "- report — 生成周报", "- review — ", "- test — "} {
		if !strings.Contains(listing, want) {
			t.Errorf("listing misses %q:\n%s", want, listing)
		}
	}
	if strings.Contains(listing, "正文") {
		t.Errorf("the listing must carry descriptions, not bodies:\n%s", listing)
	}
	if got := (&Catalog{}).Listing(); got != "" {
		t.Errorf("an empty catalog lists %q, want nothing", got)
	}
}

func TestCatalogRefresh(t *testing.T) {
	_, project, cwd := tiers(t)
	writeSkill(t, project, "hi.md", "---\ndescription: hi\n---\nbody\n")
	catalog := Load(cwd)

	if changed, _ := catalog.Refresh(); changed {
		t.Error("an untouched catalog must not rescan")
	}

	writeSkill(t, project, "bye.md", "---\ndescription: bye\n---\nbody\n")
	if changed, _ := catalog.Refresh(); !changed {
		t.Error("a new file must rescan")
	}
	if catalog.Get("bye") == nil {
		t.Error("the new file was not loaded")
	}

	writeSkill(t, project, "hi.md", "---\ndescription: hi, edited\n---\nbody\n")
	if changed, _ := catalog.Refresh(); !changed {
		t.Error("an edited entry must rescan")
	}
	if got := catalog.Get("hi").Description; got != "hi, edited" {
		t.Errorf("the edited description is %q", got)
	}

	// A bundle appearing inside a directory that already existed changes no
	// root's modification time: the scan has to look one level down.
	if err := os.MkdirAll(filepath.Join(project, "pack"), 0o755); err != nil {
		t.Fatal(err)
	}
	catalog.Refresh()
	writeSkill(t, project, "pack/SKILL.md", "---\ndescription: pack\n---\nbody\n")
	if changed, _ := catalog.Refresh(); !changed {
		t.Error("a bundle entry appearing must rescan")
	}
	if catalog.Get("pack") == nil {
		t.Error("the bundle was not loaded")
	}

	if err := os.Remove(filepath.Join(project, "bye.md")); err != nil {
		t.Fatal(err)
	}
	if changed, _ := catalog.Refresh(); !changed {
		t.Error("a removed file must rescan")
	}
	if catalog.Get("bye") != nil {
		t.Error("the removed file is still loaded")
	}
}

func TestStatePending(t *testing.T) {
	_, project, cwd := tiers(t)
	writeSkill(t, project, "hi.md", "---\ndescription: hi\n---\nbody\n")
	state := NewState(Load(cwd))

	writeSkill(t, project, "bye.md", "---\ndescription: 部署到预发环境\n---\nbody\n")
	if err := os.Remove(filepath.Join(project, "hi.md")); err != nil {
		t.Fatal(err)
	}
	if changed, _ := state.Refresh(); !changed {
		t.Fatal("a rescan that saw two moves must report a change")
	}

	change := state.Pending()
	if len(change.Added) != 1 || change.Added[0].Name != "bye" {
		t.Errorf("Added = %+v, want just bye", change.Added)
	}
	if !slices.Equal(change.Removed, []string{"hi"}) {
		t.Errorf("Removed = %q, want [hi]", change.Removed)
	}
	notice := change.Notice()
	for _, want := range []string{"Added: /bye — 部署到预发环境", "Removed: /hi"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice misses %q:\n%s", want, notice)
		}
	}
	if again := state.Pending().Notice(); again != "" {
		t.Errorf("the notice is one-shot, but it came back as %q", again)
	}
}
