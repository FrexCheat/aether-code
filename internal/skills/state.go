package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/FrexCheat/aether-code/internal/command"
)

// Marker prefix and the standing second line every delivered block carries.
const (
	markerPrefix = "[aether:skill "

	// blockLead reads the same whether the block arrived as a tool result or
	// as a message: it says what the block is, that it replaces an earlier one
	// of the same name, and which way a conflict with the general conventions
	// at the end of the context goes.
	blockLead = "以下是由系统按请求加载的能力包指令，请按它执行本次任务。" +
		"它取代同名的较早块；与后文的通用约定冲突时以它为准。"
)

// Caps on what one delivery and one recovery attachment may cost.
const (
	// maxBlockBytes bounds a single block; the entry file sits inside an
	// allowed root, so a cut body is recoverable by reading it.
	maxBlockBytes = 24 * 1024
	// maxRecoveryBytes is the whole recovery attachment's budget. A compaction
	// is not the place to rebuild everything a long session ever activated.
	maxRecoveryBytes = 48 * 1024
	// maxBundleFiles bounds the resource listing a bundle contributes.
	maxBundleFiles = 10
)

// recoveryLead opens the compaction attachment, and recoveryDropped says so
// when the budget cut blocks off. Both are model-facing text, so Chinese.
const (
	recoveryLead = "## 压缩前已加载的能力包指令\n\n" +
		"下面的指令在本会话中已被显式加载，上文的摘要没有它们。按它们继续执行。"
	recoveryDropped = "（另有 %d 条较早激活的指令因篇幅未列出；" +
		"需要时用 Skill 工具重新加载。）"
)

// Activation is one activated Skill and the arguments it was activated with.
type Activation struct {
	Name string
	Args string
}

// Change is what one rescan found: the Skills that appeared and the names that
// disappeared.
type Change struct {
	Added   []*Skill
	Removed []string
}

// State is the session's record of what is active. It has to exist because a
// compaction can eat a delivered block and tell nobody: the block is the
// instructions, and a user who never cancelled the activation still expects
// them to be followed. The ledger is runtime state — it starts empty on a
// resume and disappears with the process — and it is also what /skills reads.
type State struct {
	cat *Catalog

	mu      sync.RWMutex
	active  []Activation
	known   map[string]struct{}
	added   []*Skill
	removed []string
}

func NewState(cat *Catalog) *State {
	s := &State{cat: cat, known: make(map[string]struct{})}
	for _, skill := range cat.List() {
		s.known[skill.Name] = struct{}{}
	}
	return s
}

// Deliver renders an activation's delivery block and records it, in that
// order: the instructions have to exist before the ledger claims them, or a
// definition that cannot be read would leave the session reporting an
// activation that was never delivered.
func (s *State) Deliver(a Activation) (string, error) {
	skill := s.Get(a.Name)
	if skill == nil {
		return "", fmt.Errorf("unknown skill %q", a.Name)
	}
	block, err := renderBlock(skill, a.Args)
	if err != nil {
		return "", err
	}
	s.record(a)
	return block, nil
}

// record files an activation. The same name again keeps its place in the order
// and takes the new arguments; nothing is written to the history here.
func (s *State) record(a Activation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.active {
		if s.active[i].Name == a.Name {
			s.active[i].Args = a.Args
			return
		}
	}
	s.active = append(s.active, a)
}

// Active lists the activations in activation order.
func (s *State) Active() []Activation {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.active)
}

// Clear drops every activation and reports how many there were.
func (s *State) Clear() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.active)
	s.active = nil
	return n
}

// Get resolves a name against the catalog as it stands now.
func (s *State) Get(name string) *Skill {
	if s == nil || s.cat == nil {
		return nil
	}
	return s.cat.Get(name)
}

// List returns the loaded definitions, sorted by name.
func (s *State) List() []*Skill {
	if s == nil || s.cat == nil {
		return nil
	}
	return s.cat.List()
}

// Catalog is the loaded set behind the state, for /skills and the reload path.
func (s *State) Catalog() *Catalog { return s.cat }

// Refresh rescans the directories and records which Skills appeared or
// disappeared, for the one-shot notice Pending returns.
func (s *State) Refresh() (changed bool, notes []string) {
	if s == nil || s.cat == nil {
		return false, nil
	}
	changed, notes = s.cat.Refresh()
	if !changed {
		return false, nil
	}
	loaded := s.cat.List()
	names := make(map[string]struct{}, len(loaded))
	for _, skill := range loaded {
		names[skill.Name] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, skill := range loaded {
		if _, ok := s.known[skill.Name]; !ok {
			s.added = append(s.added, skill)
		}
	}
	for name := range s.known {
		if _, ok := names[name]; !ok {
			s.removed = append(s.removed, name)
		}
	}
	slices.Sort(s.removed)
	s.known = names
	return true, notes
}

// Pending returns the change recorded since the last call and clears it: the
// notice is a one-off announcement, not a standing state.
func (s *State) Pending() Change {
	if s == nil {
		return Change{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	change := Change{Added: s.added, Removed: s.removed}
	s.added, s.removed = nil, nil
	return change
}

// Notice renders the change as the message the model is told about it with; an
// empty string means there is nothing to announce.
func (c Change) Notice() string {
	if len(c.Added) == 0 && len(c.Removed) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("会话开始后 Skill 目录发生了变化：")
	for _, skill := range c.Added {
		fmt.Fprintf(&b, "\nAdded: /%s — %s", skill.Name, skill.Description)
	}
	for _, name := range c.Removed {
		fmt.Fprintf(&b, "\nRemoved: /%s", name)
	}
	return b.String()
}

// RecoveryBlocks renders the instructions that are still in force, for the
// compaction boundary to splice in: newest activation first, so a tight budget
// drops the least recent one. The provider reads only — compaction calls it
// while the history is being rewritten.
func (s *State) RecoveryBlocks() string {
	active := s.Active()
	if len(active) == 0 {
		return ""
	}
	var blocks []string
	dropped := 0
	used := 0
	for _, a := range slices.Backward(active) {
		skill := s.Get(a.Name)
		if skill == nil {
			continue
		}
		text, err := renderBlock(skill, a.Args)
		if err != nil {
			continue
		}
		if used+len(text) > maxRecoveryBytes {
			dropped++
			continue
		}
		used += len(text)
		blocks = append(blocks, text)
	}
	if len(blocks) == 0 && dropped == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(recoveryLead)
	for _, text := range blocks {
		b.WriteString("\n\n")
		b.WriteString(text)
	}
	if dropped > 0 {
		b.WriteString("\n\n")
		b.WriteString(fmt.Sprintf(recoveryDropped, dropped))
	}
	return b.String()
}

// Marker is the block's first line: the name, then the arguments to the end of
// the line. A name never carries whitespace, so the first space after it splits
// the two.
func Marker(name, args string) string {
	line := markerPrefix + name + "]"
	if args != "" {
		line += " " + args
	}
	return line
}

// ParseMarker reads a marker line back. Anything else reports false.
func ParseMarker(line string) (name, args string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimRight(line, "\r"), markerPrefix)
	if !found {
		return "", "", false
	}
	name, args, found = strings.Cut(rest, "]")
	if !found || name == "" {
		return "", "", false
	}
	return name, strings.TrimSpace(args), true
}

// IsBlock reports whether content is a delivered block, which is exactly
// whether its first line is a marker.
func IsBlock(content string) bool {
	line, _, _ := strings.Cut(content, "\n")
	_, _, ok := ParseMarker(line)
	return ok
}

// renderBlock assembles one delivery block — the marker, the standing sentence
// and the body with $ARGUMENTS expanded — and caps it. The same text is a tool
// result, a user message or a recovery attachment; the marker is the one thing
// that identifies it wherever it lands.
func renderBlock(skill *Skill, args string) (string, error) {
	body, err := skill.Body()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("skill %q has no body to activate", skill.Name)
	}
	var b strings.Builder
	b.WriteString(Marker(skill.Name, args))
	b.WriteString("\n")
	b.WriteString(blockLead)
	b.WriteString("\n\n")
	b.WriteString(command.Expand(body, args))
	if skill.Dir != "" {
		b.WriteString("\n\n资源目录：" + skill.Dir)
		if files := bundleFiles(skill.Dir); len(files) > 0 {
			b.WriteString("\n包内文件：" + strings.Join(files, ", "))
		}
	}
	return truncate(b.String(), maxBlockBytes, skill.Path), nil
}

// bundleFiles lists a directory-shaped Skill's other files, relative to its
// root, capped: they are what the body's scripts/ and reference/ paths point
// at, so a truncated list still tells the model where to look.
func bundleFiles(dir string) []string {
	var files []string
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || len(files) >= maxBundleFiles {
			return filepath.SkipAll
		}
		if entry.IsDir() {
			return nil
		}
		if rel, err := filepath.Rel(dir, path); err == nil && rel != entryFile {
			files = append(files, rel)
		}
		return nil
	})
	return files
}
