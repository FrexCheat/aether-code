package permissions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

type Rule struct {
	Tool    string
	Pattern string
	Effect  Effect
}

func (r Rule) String() string {
	return r.Tool + "(" + r.Pattern + ")"
}

// toolNames are the tool names a rule may use — the same ones the tools
// report, so a rule and a tool call are compared without translation.
var toolNames = []string{"Bash", "Read", "Write", "Edit", "Glob", "Grep"}

func ParseRule(raw string, eff Effect) (Rule, error) {
	trimmed := strings.TrimSpace(raw)
	open := strings.Index(trimmed, "(")
	if open <= 0 || !strings.HasSuffix(trimmed, ")") {
		return Rule{}, fmt.Errorf("invalid rule %q: expected Tool(pattern)", raw)
	}
	tool := strings.TrimSpace(trimmed[:open])
	pattern := trimmed[open+1 : len(trimmed)-1]
	if pattern == "" {
		return Rule{}, fmt.Errorf("invalid rule %q: pattern is empty", raw)
	}
	if !slices.Contains(toolNames, tool) {
		return Rule{}, fmt.Errorf("invalid rule %q: unknown tool %q, expected one of %s",
			raw, tool, strings.Join(toolNames, ", "))
	}
	return Rule{Tool: tool, Pattern: pattern, Effect: eff}, nil
}

func (r Rule) Matches(toolName, content string) bool {
	if r.Tool != toolName {
		return false
	}
	if !strings.ContainsAny(r.Pattern, "*?") {
		return r.Pattern == content
	}
	var b strings.Builder
	b.WriteString(`(?s)\A`)
	for _, ch := range r.Pattern {
		switch ch {
		case '*':
			b.WriteString(`.*`)
		case '?':
			b.WriteString(`.`)
		default:
			b.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	b.WriteString(`\z`)
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(content)
}

func ExtractContent(toolName string, args map[string]any) string {
	var field string
	switch toolName {
	case "Bash":
		field = "command"
	case "Read", "Write", "Edit":
		field = "file_path"
	case "Glob", "Grep":
		field = "pattern"
	default:
		return ""
	}
	v, _ := args[field].(string)
	return v
}

// EvaluateRules decides a call against a rule set. deny outranks ask outranks
// allow, regardless of file or line order; nil means no rule matched.
func EvaluateRules(rules []Rule, toolName, content string) *Decision {
	var hit *Decision
	for _, r := range rules {
		if !r.Matches(toolName, content) {
			continue
		}
		switch r.Effect {
		case Deny:
			// Nothing outranks deny, so this is already the answer.
			return &Decision{Effect: Deny, Reason: "Permission denied by rule " + r.String()}
		case Ask:
			if hit == nil || hit.Effect == Allow {
				hit = &Decision{Effect: Ask, Reason: "rule " + r.String() + " asks for confirmation"}
			}
		case Allow:
			if hit == nil {
				hit = &Decision{Effect: Allow, Reason: "allowed by rule " + r.String()}
			}
		}
	}
	return hit
}

// cachedFile is one rule file's parse result. modTime and size together decide
// whether a re-read is needed: mtime alone is not enough, since a rewrite
// within the same timestamp granularity can leave it unchanged.
type cachedFile struct {
	modTime time.Time
	size    int64
	rules   []Rule
}

// RuleEngine reads the three rule files (user, project, local) and merges them
// into one rule set. Files are parsed once and re-read only after they change,
// so a decision costs a few stat calls and edits take effect without a restart.
type RuleEngine struct {
	userPath    string
	projectPath string
	localPath   string

	mu    sync.Mutex
	cache map[string]cachedFile
}

// NewRuleEngine locates the three rule files under workDir and, when
// resolvable, the user's home directory. Missing files are simply empty rules.
func NewRuleEngine(workDir string) *RuleEngine {
	e := &RuleEngine{
		projectPath: filepath.Join(workDir, ".aethercode", "permissions.yaml"),
		localPath:   filepath.Join(workDir, ".aethercode", "permissions.local.yaml"),
		cache:       map[string]cachedFile{},
	}
	if home, err := os.UserHomeDir(); err == nil {
		e.userPath = filepath.Join(home, ".aethercode", "permissions.yaml")
	}
	return e
}

// Snapshot returns the merged rules of all three files.
func (e *RuleEngine) Snapshot() []Rule {
	var all []Rule
	for _, path := range e.paths() {
		all = append(all, e.load(path).rules...)
	}
	return all
}

func (e *RuleEngine) paths() []string {
	return []string{e.userPath, e.projectPath, e.localPath}
}

// load returns a file's cached parse result, re-reading it when its modTime or
// size changed. A file that cannot be stat'ed or read is treated as empty and
// its stale cache entry is dropped, so a deleted file stops contributing.
func (e *RuleEngine) load(path string) cachedFile {
	if path == "" {
		return cachedFile{}
	}
	info, err := os.Stat(path)
	if err != nil {
		e.mu.Lock()
		delete(e.cache, path)
		e.mu.Unlock()
		return cachedFile{}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if c, ok := e.cache[path]; ok && c.modTime.Equal(info.ModTime()) && c.size == info.Size() {
		return c
	}
	c := parseRulesFile(path)
	c.modTime = info.ModTime()
	c.size = info.Size()
	e.cache[path] = c
	return c
}

// rulesFile is the on-disk document shape shared by all three files.
type rulesFile struct {
	Rules []ruleEntry `yaml:"rules,omitempty"`
}

type ruleEntry struct {
	Rule   string `yaml:"rule"`
	Effect string `yaml:"effect"`
}

// parseRulesFile reads one file. Anything unreadable or unparsable degrades to
// an empty file; a single bad entry is skipped and never sinks the others.
func parseRulesFile(path string) cachedFile {
	data, err := os.ReadFile(path)
	if err != nil {
		return cachedFile{}
	}
	var doc rulesFile
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return cachedFile{}
	}
	var cf cachedFile
	for _, entry := range doc.Rules {
		eff := Effect(strings.ToLower(strings.TrimSpace(entry.Effect)))
		if eff != Allow && eff != Ask && eff != Deny {
			continue
		}
		rule, err := ParseRule(entry.Rule, eff)
		if err != nil {
			continue
		}
		cf.rules = append(cf.rules, rule)
	}
	return cf
}

// AppendLocalRule adds one rule to the local file. This is the process writing
// its own configuration after the user granted a permanent permission — it
// does not pass through the checker, so the blacklist's protection of the rule
// files does not apply here.
func (e *RuleEngine) AppendLocalRule(r Rule) error {
	doc, err := e.readLocal()
	if err != nil {
		return err
	}
	doc.Rules = append(doc.Rules, ruleEntry{Rule: r.String(), Effect: string(r.Effect)})
	return e.writeLocal(doc)
}

func (e *RuleEngine) readLocal() (rulesFile, error) {
	if e.localPath == "" {
		return rulesFile{}, errors.New("no local permission file path")
	}
	data, err := os.ReadFile(e.localPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return rulesFile{}, nil
		}
		return rulesFile{}, err
	}
	var doc rulesFile
	// Rewriting a file we could not parse would throw away whatever the user
	// hand-wrote in it, so the failure is reported instead.
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return rulesFile{}, fmt.Errorf("%s: %w", e.localPath, err)
	}
	return doc, nil
}

func (e *RuleEngine) writeLocal(doc rulesFile) error {
	if e.localPath == "" {
		return errors.New("no local permission file path")
	}
	if err := os.MkdirAll(filepath.Dir(e.localPath), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	if err := os.WriteFile(e.localPath, data, 0o644); err != nil {
		return err
	}
	e.mu.Lock()
	delete(e.cache, e.localPath) // force the next read to see what we just wrote
	e.mu.Unlock()
	return nil
}
