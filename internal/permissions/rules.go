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

	"github.com/FrexCheat/aether-code/internal/config"
)

type Rule struct {
	Tool    string
	Pattern string
	Effect  Effect
}

type cachedFile struct {
	modTime time.Time
	size    int64
	rules   []Rule
}

type RuleEngine struct {
	userPath  string
	localPath string

	mu    sync.Mutex
	cache map[string]cachedFile
}

type ruleEntry struct {
	Rule   string `yaml:"rule"`
	Effect string `yaml:"effect"`
}

type rulesFile struct {
	Rules []ruleEntry `yaml:"rules,omitempty"`
}

func (r Rule) String() string {
	return r.Tool + "(" + r.Pattern + ")"
}

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
	if !knownToolName(tool) {
		return Rule{}, fmt.Errorf("invalid rule %q: unknown tool %q, expected one of %s",
			raw, tool, strings.Join(toolNames, ", "))
	}
	return Rule{Tool: tool, Pattern: pattern, Effect: eff}, nil
}

// 将模型返回的 Pattern 转换为 Rule 里的 Pattern，主要是对 * 和 \ 进行转义
func escapeRulePattern(text string) string {
	return strings.NewReplacer(`\`, `\\`, `*`, `\*`).Replace(text)
}

func patternMatches(pattern, value string) bool {
	runes := []rune(pattern)
	var source strings.Builder
	source.WriteString(`\A`)
	for i := 0; i < len(runes); {
		switch {
		case runes[i] == '\\' && i+1 < len(runes):
			source.WriteString(regexp.QuoteMeta(string(runes[i+1])))
			i += 2
		case runes[i] == '*':
			source.WriteString(`.*`)
			i++
		default:
			source.WriteString(regexp.QuoteMeta(string(runes[i])))
			i++
		}
	}
	source.WriteString(`\z`)
	re, err := regexp.Compile(source.String())
	return err == nil && re.MatchString(value)
}

// knownToolName accepts the built-in vocabulary plus any mcp__-qualified
// name: the dialog records grants for MCP tools by their own name, and a
// rule the dialog wrote must survive a reload.
func knownToolName(tool string) bool {
	if slices.Contains(toolNames, tool) {
		return true
	}
	rest, ok := strings.CutPrefix(tool, "mcp__")
	return ok && rest != ""
}

func (r Rule) Matches(toolName, value string) bool {
	// The tool name goes through the same matcher as the argument: builtin
	// names hold no pattern characters, so this equals equality for them,
	// while mcp__server__* covers every tool of one server.
	return patternMatches(r.Tool, toolName) && patternMatches(r.Pattern, value)
}

func EvaluateRules(rules []Rule, toolName, content string) *Decision {
	var hit *Decision
	for _, r := range rules {
		if !r.Matches(toolName, content) {
			continue
		}
		switch r.Effect {
		case Deny:
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

func NewRuleEngine(workDir string) *RuleEngine {
	e := &RuleEngine{
		localPath: filepath.Join(workDir, config.ProjectDir, "permissions.local.yaml"),
		cache:     map[string]cachedFile{},
	}
	if root := config.DataRoot(); root != "" {
		e.userPath = filepath.Join(root, "permissions.yaml")
	}
	return e
}

func (e *RuleEngine) Snapshot() []Rule {
	var all []Rule
	for _, path := range e.paths() {
		all = append(all, e.load(path).rules...)
	}
	return all
}

func (e *RuleEngine) paths() []string {
	return []string{e.userPath, e.localPath}
}

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
	if cached, ok := e.cache[path]; ok && cached.modTime.Equal(info.ModTime()) && cached.size == info.Size() {
		return cached
	}
	cached := parseRulesFile(path)
	cached.modTime = info.ModTime()
	cached.size = info.Size()
	e.cache[path] = cached
	return cached
}

func parseRulesFile(path string) cachedFile {
	data, err := os.ReadFile(path)
	if err != nil {
		return cachedFile{}
	}
	var doc rulesFile
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return cachedFile{}
	}
	var file cachedFile
	for _, entry := range doc.Rules {
		eff := Effect(strings.ToLower(strings.TrimSpace(entry.Effect)))
		if eff != Allow && eff != Ask && eff != Deny {
			continue
		}
		rule, err := ParseRule(entry.Rule, eff)
		if err != nil {
			continue
		}
		file.rules = append(file.rules, rule)
	}
	return file
}

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
