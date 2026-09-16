// Package prompt owns the model-facing instruction text: the modular system
// prompt assembled at startup, and the bodies of the reminders injected into
// the conversation at runtime.
//
// This is a content layer. It decides what the model reads, not how the text
// reaches it — delivery (system block vs. message), caching breakpoints and
// wire translation all belong to the llm adapters.
package prompt

import (
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
)

type Section struct {
	Name     string
	Priority int
	Content  string
}

type Builder struct {
	sections []Section
}

type EnvironmentContext struct {
	WorkDir   string
	OS        string
	Arch      string
	Shell     string
	IsGitRepo bool
	GitBranch string
	Date      string
}

func NewBuilder() *Builder {
	return &Builder{}
}

func (b *Builder) Add(s Section) *Builder {
	b.sections = append(b.sections, s)
	return b
}

func (b *Builder) Build() string {
	sorted := make([]Section, len(b.sections))
	copy(sorted, b.sections)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Priority < sorted[j].Priority
	})

	parts := make([]string, 0, len(sorted))
	for _, s := range sorted {
		if content := strings.TrimSpace(s.Content); content != "" {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n\n")
}

func BuildSystemPrompt(env EnvironmentContext) string {
	return NewBuilder().
		Add(identitySection()).
		Add(systemSection()).
		Add(doingTasksSection()).
		Add(executingActionsSection()).
		Add(usingToolsSection()).
		Add(toneStyleSection()).
		Add(textOutputSection()).
		Add(environmentSection(env)).
		Build()
}

func DetectEnvironment(workDir string) EnvironmentContext {
	env := EnvironmentContext{
		WorkDir: workDir,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
		Shell:   os.Getenv("SHELL"),
		Date:    time.Now().Format("2006-01-02"),
	}
	if env.Shell == "" {
		env.Shell = "bash"
	}

	out, err := exec.Command("git", "-C", workDir, "rev-parse", "--is-inside-work-tree").Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return env
	}
	env.IsGitRepo = true

	// A repository with no commits yet has no branch to report; leave it empty.
	if branch, err := exec.Command("git", "-C", workDir, "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		env.GitBranch = strings.TrimSpace(string(branch))
	}
	return env
}
