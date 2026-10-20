// Package skills loads Skill definitions — Markdown files whose frontmatter
// names a reusable procedure and whose body is that procedure — from three
// tiers, and keeps the session's record of which ones have been activated.
//
// Loading is two-phase on purpose: a scan reads only the frontmatter, so what
// the model is offered costs a name and a sentence, and the body is read again
// when the Skill is activated. Nothing here talks to the model or to the
// conversation beyond handing a block of text back to its caller.
package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Source names the tier a definition came from; a higher tier replaces a lower
// one of the same name outright, rather than merging field by field.
type Source string

const (
	SourceBuiltin Source = "builtin"
	SourceUser    Source = "user"
	SourceProject Source = "project"
)

// entryFile is the entry point of a directory-shaped Skill; the single-file
// shape is the file itself.
const entryFile = "SKILL.md"

// fence opens and closes the frontmatter block.
const fence = "---"

// descriptionMax bounds the display field; the frontmatter's one-liner is the
// whole of what stage one costs.
const descriptionMax = 200

// Skill is one definition: everything stage one reads, plus where the body
// lives so that activation can read it again.
type Skill struct {
	Name        string
	Description string
	Source      Source
	Path        string // the entry file
	Dir         string // the directory shape's root; empty for a single file
}

// frontmatter is the YAML block at the top of an entry file. Unknown fields are
// ignored rather than rejected: a file written for a later version still loads.
type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// Parse reads one entry file: the frontmatter between fences and the body after
// them. A missing frontmatter is not fatal — the name falls back to the file's
// own and the description to the body's first plain line — but a malformed one
// is, so a typo never quietly turns one Skill into another.
func Parse(path string, source Source, data []byte) (*Skill, string, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	head, body, err := splitFrontmatter(text)
	switch {
	case errors.Is(err, errNoFrontmatter):
		head = ""
	case err != nil:
		return nil, "", err
	}

	var fm frontmatter
	if strings.TrimSpace(head) != "" {
		if err := yaml.Unmarshal([]byte(head), &fm); err != nil {
			return nil, "", fmt.Errorf("frontmatter: %w", err)
		}
	}

	name := strings.TrimSpace(fm.Name)
	if name == "" {
		name = fallbackName(path)
	}
	if err := checkName(name); err != nil {
		return nil, "", err
	}

	skill := &Skill{
		Name:        name,
		Description: describe(fm.Description, body),
		Source:      source,
		Path:        path,
	}
	if filepath.Base(path) == entryFile {
		skill.Dir = filepath.Dir(path)
	}
	return skill, body, nil
}

// Body re-reads the entry file's body, so an edit takes effect on the next
// activation without a restart. A file edited into something unparseable fails
// here rather than at load: the definition that was loaded is the one that
// counts.
func (s *Skill) Body() (string, error) {
	data, err := s.read()
	if err != nil {
		return "", err
	}
	_, body, err := Parse(s.Path, s.Source, data)
	if err != nil {
		return "", fmt.Errorf("%s: %w", s.Path, err)
	}
	return body, nil
}

func (s *Skill) read() ([]byte, error) {
	if s.Source == SourceBuiltin {
		return builtinFS.ReadFile(s.Path)
	}
	return os.ReadFile(s.Path)
}

// errNoFrontmatter reports a file that does not open with a fence; that is not
// a failure, only a definition that declares nothing.
var errNoFrontmatter = errors.New("no frontmatter")

// splitFrontmatter cuts a leading fence block off the text. The opening fence
// must be the first line; a fence that never closes is an error, because
// treating the rest of the file as frontmatter would swallow the body.
func splitFrontmatter(text string) (head, body string, err error) {
	line, rest, ok := cutLine(text)
	if !ok || line != fence {
		return "", strings.TrimLeft(text, "\n"), errNoFrontmatter
	}
	var b strings.Builder
	for rest != "" {
		line, next, _ := cutLine(rest)
		rest = next
		if line == fence {
			return b.String(), strings.TrimLeft(rest, "\n"), nil
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return "", "", errors.New("frontmatter fence is never closed")
}

// cutLine splits one line off, terminator excluded; ok is false on empty input.
func cutLine(text string) (line, rest string, ok bool) {
	if text == "" {
		return "", "", false
	}
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i], text[i+1:], true
	}
	return text, "", true
}

// fallbackName is the name a definition takes when its frontmatter leaves it
// out: the file's own, or the directory's for a bundle entry.
func fallbackName(path string) string {
	base := filepath.Base(path)
	if base == entryFile {
		return filepath.Base(filepath.Dir(path))
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// checkName keeps a name usable both as a slash spelling and as the marker's
// first field: not empty, no whitespace, no leading slash.
func checkName(name string) error {
	switch {
	case name == "":
		return errors.New("name is empty")
	case strings.HasPrefix(name, "/"):
		return fmt.Errorf("name %q must not start with /", name)
	case strings.IndexFunc(name, unicode.IsSpace) >= 0:
		return fmt.Errorf("name %q must not contain whitespace", name)
	}
	return nil
}

// describe takes the frontmatter's line, or the body's first line that is
// neither blank nor a heading. It is a display field, so an over-long one is
// cut rather than rejected.
func describe(declared, body string) string {
	desc := strings.TrimSpace(declared)
	if desc == "" {
		for _, line := range strings.Split(body, "\n") {
			if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			desc = line
			break
		}
	}
	desc = strings.Join(strings.Fields(desc), " ")
	if runes := []rune(desc); len(runes) > descriptionMax {
		return string(runes[:descriptionMax]) + "…"
	}
	return desc
}

// truncate cuts text to a byte budget without splitting a rune and says where
// the rest is: the entry file sits inside an allowed root, so a truncated block
// is recoverable rather than lost.
func truncate(text string, budget int, path string) string {
	if len(text) <= budget {
		return text
	}
	cut := budget
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "\n… (truncated — read " + path + " for the rest)"
}
