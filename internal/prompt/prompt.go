package prompt

import (
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Environment is the session context the prompt reports to the model.
type Environment struct {
	WorkDir   string
	OS        string
	Arch      string
	Shell     string
	IsGitRepo bool
	GitBranch string
	Date      string
}

// section is one block of the system prompt, ordered by priority.
type section struct {
	priority int
	content  string
}

func DetectEnvironment(workDir string) Environment {
	env := Environment{
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

func BuildSystemPrompt(env Environment) string {
	sections := []section{
		identitySection(),
		systemSection(),
		doingTasksSection(),
		executingActionsSection(),
		usingToolsSection(),
		externalToolsSection(),
		toneStyleSection(),
		textOutputSection(),
		environmentSection(env),
	}
	slices.SortStableFunc(sections, func(a, b section) int { return a.priority - b.priority })

	parts := make([]string, 0, len(sections))
	for _, s := range sections {
		if content := strings.TrimSpace(s.content); content != "" {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n\n")
}
