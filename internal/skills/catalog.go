package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/FrexCheat/aether-code/internal/config"
)

// Catalog is the loaded set of Skills, one per name, highest tier winning. It
// is read by the model's tool, by the slash commands and by the status
// readouts, so every accessor takes the lock and a rescan swaps the whole set
// at once — a reader never sees half of a rebuild.
type Catalog struct {
	cwd string

	mu     sync.RWMutex
	skills []*Skill
	byName map[string]*Skill
	notes  []string
	fp     string
}

// Load scans the three tiers once and returns the catalog. A directory that
// does not exist contributes nothing, and a file that cannot be used costs only
// itself: one broken Skill must not leave the session without any.
func Load(cwd string) *Catalog {
	c := &Catalog{cwd: cwd}
	c.reload()
	return c
}

// Refresh rescans the tiers when the directories have moved on since the last
// scan and reports whether it did. The check reads no definition — it only
// enumerates and stats the entries — so running it before every submitted line
// is free.
func (c *Catalog) Refresh() (changed bool, notes []string) {
	fp := fingerprint(c.cwd)
	c.mu.RLock()
	same := fp == c.fp
	c.mu.RUnlock()
	if same {
		return false, nil
	}
	c.reload()
	return true, c.Notes()
}

// Get returns the definition a name resolves to, or nil.
func (c *Catalog) Get(name string) *Skill {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.byName[name]
}

// List returns every definition, sorted by name.
func (c *Catalog) List() []*Skill {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.skills
}

// Notes reports what the last scan skipped, one line each. It is what makes
// "a broken file only costs itself" honest: a silent skip would leave a user
// believing they have a Skill.
func (c *Catalog) Notes() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.notes
}

// Listing renders the names and one-line descriptions for the system prompt,
// one bullet per Skill, in name order. It is the whole of what stage one costs.
func (c *Catalog) Listing() string {
	skills := c.List()
	lines := make([]string, 0, len(skills))
	for _, skill := range skills {
		lines = append(lines, "- "+skill.Name+" — "+skill.Description)
	}
	return strings.Join(lines, "\n")
}

// reload rescans every tier and swaps the result in.
func (c *Catalog) reload() {
	var notes []string
	builtin, builtinNotes := scanBuiltins()
	notes = append(notes, builtinNotes...)

	byName := make(map[string]*Skill)
	for _, tier := range []struct {
		root   string
		source Source
	}{
		{userSkillsDir(), SourceUser},
		{projectSkillsDir(c.cwd), SourceProject},
	} {
		tierSkills, tierNotes := scanDir(tier.root, tier.source)
		notes = append(notes, tierNotes...)
		// A later tier replaces an earlier one outright, name for name.
		for _, skill := range tierSkills {
			byName[skill.Name] = skill
		}
	}
	for _, skill := range builtin {
		if _, taken := byName[skill.Name]; !taken {
			byName[skill.Name] = skill
		}
	}

	skills := make([]*Skill, 0, len(byName))
	for _, skill := range byName {
		skills = append(skills, skill)
	}
	slices.SortFunc(skills, func(a, b *Skill) int { return strings.Compare(a.Name, b.Name) })

	c.mu.Lock()
	defer c.mu.Unlock()
	c.skills = skills
	c.byName = byName
	c.notes = notes
	c.fp = fingerprint(c.cwd)
}

// scanDir reads one tier's directory. Nothing here fails the scan: a missing
// directory contributes nothing, and within a tier the first file to claim a
// name keeps it — the later one is a note, not an override.
func scanDir(root string, source Source) ([]*Skill, []string) {
	if root == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, nil
	}
	var skills []*Skill
	var notes []string
	taken := make(map[string]string)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		path := ""
		switch {
		case entry.IsDir():
			path = filepath.Join(root, name, entryFile)
			if _, err := os.Stat(path); err != nil {
				notes = append(notes, fmt.Sprintf("skills: %s: no %s, skipped", filepath.Join(root, name), entryFile))
				continue
			}
		case strings.HasSuffix(name, ".md"):
			path = filepath.Join(root, name)
		default:
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			notes = append(notes, fmt.Sprintf("skills: %s: %v", path, err))
			continue
		}
		skill, _, err := Parse(path, source, data)
		if err != nil {
			notes = append(notes, fmt.Sprintf("skills: %s: %v, skipped", path, err))
			continue
		}
		if prev, dup := taken[skill.Name]; dup {
			notes = append(notes, fmt.Sprintf("skills: %s: name %q is already taken by %s, skipped", path, skill.Name, prev))
			continue
		}
		taken[skill.Name] = path
		skills = append(skills, skill)
	}
	return skills, notes
}

// fingerprint identifies one state of the two on-disk tiers: every entry path
// with its modification time and size. The roots are enumerated rather than
// merely statted because a bundle whose directory already exists changes no
// root's modification time when its entry file appears or vanishes. Bodies are
// deliberately absent: they are read at activation, not at load.
func fingerprint(cwd string) string {
	var b strings.Builder
	for _, root := range []string{userSkillsDir(), projectSkillsDir(cwd)} {
		if root == "" {
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			var path string
			switch {
			case entry.IsDir():
				path = filepath.Join(root, name, entryFile)
			case strings.HasSuffix(name, ".md"):
				path = filepath.Join(root, name)
			default:
				continue
			}
			info, err := os.Stat(path)
			if err != nil {
				fmt.Fprintf(&b, "%s\t-\n", path)
				continue
			}
			fmt.Fprintf(&b, "%s\t%d\t%d\n", path, info.ModTime().UnixNano(), info.Size())
		}
	}
	return b.String()
}

// userSkillsDir is the user-level tier, empty when no home directory resolves.
func userSkillsDir() string {
	root := config.DataRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "skills")
}

// projectSkillsDir is the project tier: the same runtime directory as the
// settings file, scoped to the working directory rather than to the repository
// root.
func projectSkillsDir(cwd string) string {
	if cwd == "" {
		return ""
	}
	return filepath.Join(cwd, config.ProjectDir, "skills")
}
