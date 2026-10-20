package prompt

import (
	"os"
	"os/exec"
	"runtime"
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

// BuildSystemPrompt assembles the session's system prompt once at startup.
// The literal order is the prompt order — project instructions ride right
// after the identity, the environment closes — and an empty section drops
// out, so a section that has nothing to say costs nothing.
//
// skillList is the available-skills listing, empty when there is none. It is
// passed in rather than read from a package here because the caller already
// holds the loaded catalog; the prompt is assembled exactly once either way,
// and a prompt that changed mid-session would invalidate the whole cached
// prefix with the history behind it.
func BuildSystemPrompt(env Environment, skillList string) string {
	sections := []string{
		identitySection(),
		projectInstructionsSection(env.WorkDir),
		systemSection(),
		doingTasksSection(),
		executingActionsSection(),
		usingToolsSection(),
		externalToolsSection(),
		availableSkillsSection(skillList),
		toneStyleSection(),
		textOutputSection(),
		environmentSection(env),
	}
	parts := make([]string, 0, len(sections))
	for _, s := range sections {
		if content := strings.TrimSpace(s); content != "" {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n\n")
}
