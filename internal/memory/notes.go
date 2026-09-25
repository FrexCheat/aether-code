package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	indexFileName = "MEMORY.md"
	indexMaxLines = 200
	indexMaxBytes = 25000

	maxNoteBody    = 8192
	maxDescription = 200
	maxNoteName    = 64
	noteTypeOther  = "project" // unknown types normalize here: dropping knowledge over an enum is worse
)

var (
	slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	noteTypes   = []string{"user", "feedback", "project", "reference"}
)

// note is one memory file: frontmatter plus body.
type note struct {
	Name        string
	Description string
	Type        string
	Body        string
	ModTime     time.Time
}

type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Type        string `yaml:"type"`
}

// readNotes loads every note in the directory; a file with unparseable
// frontmatter is skipped rather than failing the whole listing.
func readNotes(dir string) []note {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var notes []note
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".md") || name == indexFileName {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		fm, body, ok := parseFrontmatter(data)
		if !ok {
			continue
		}
		n := note{
			Name:        fm.Name,
			Description: fm.Description,
			Type:        normalizeType(fm.Type),
			Body:        body,
		}
		if info, err := entry.Info(); err == nil {
			n.ModTime = info.ModTime()
		}
		notes = append(notes, n)
	}
	slices.SortFunc(notes, func(a, b note) int { return strings.Compare(a.Name, b.Name) })
	return notes
}

func parseFrontmatter(data []byte) (frontmatter, string, bool) {
	text := string(data)
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return frontmatter{}, "", false
	}
	block, body, ok := strings.Cut(rest, "\n---")
	if !ok {
		return frontmatter{}, "", false
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(block), &fm); err != nil {
		return frontmatter{}, "", false
	}
	if fm.Name == "" {
		return frontmatter{}, "", false
	}
	return fm, strings.TrimSpace(body), true
}

func normalizeType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if slices.Contains(noteTypes, t) {
		return t
	}
	return noteTypeOther
}

// writeNote renders one note atomically: a temp file in the same directory
// renamed over the target, so a crash never leaves a half-written note.
func writeNote(dir string, n note) error {
	block, err := yaml.Marshal(frontmatter{Name: n.Name, Description: n.Description, Type: n.Type})
	if err != nil {
		return fmt.Errorf("memory: cannot encode note %s: %w", n.Name, err)
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.Write(block)
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(n.Body))
	b.WriteString("\n")
	return writeFileAtomic(filepath.Join(dir, n.Name+".md"), []byte(b.String()))
}

// writeIndex regenerates MEMORY.md deterministically from the notes: one line
// per note, capped at 200 lines and 25 KB with an explicit truncation marker.
// The index is derived, never hand-maintained, so it cannot drift from the
// notes it points at.
func writeIndex(dir string, notes []note) error {
	var b strings.Builder
	b.WriteString("# Memory\n")
	lines := 1 // the header; one more line is held back for the truncation marker
	for i, n := range notes {
		line := fmt.Sprintf("- [%s](%s.md) — %s\n", n.Name, n.Name, n.Description)
		if lines+1 >= indexMaxLines || b.Len()+len(line) > indexMaxBytes {
			fmt.Fprintf(&b, "- … (truncated: %d notes not shown)\n", len(notes)-i)
			break
		}
		b.WriteString(line)
		lines++
	}
	return writeFileAtomic(filepath.Join(dir, indexFileName), []byte(b.String()))
}

func readIndexFile(dir string) ([]byte, error) {
	return os.ReadFile(filepath.Join(dir, indexFileName))
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("memory: cannot create %s: %w", filepath.Dir(path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("memory: cannot create a temp file in %s: %w", filepath.Dir(path), err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("memory: cannot write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("memory: cannot write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("memory: cannot write %s: %w", path, err)
	}
	return nil
}

// validNoteName reports whether an extraction-provided name may become a file
// name; it is the path-traversal gate.
func validNoteName(name string) bool {
	return len(name) <= maxNoteName && slugPattern.MatchString(name)
}

func truncateRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	runes := []rune(s)
	return string(runes[:limit]) + "…"
}
