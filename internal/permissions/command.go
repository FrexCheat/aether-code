package permissions

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// subcommand is one command as the shell will run it, sliced out of the
// original text so quotes and escapes survive verbatim.
type subcommand struct {
	text string
	// opaque marks a subcommand that runs code its own text does not show as a
	// command — a substitution, a subshell, a control-flow body. A rule is
	// written against text, so it cannot speak for one of these.
	opaque bool
	// redirects are the output redirections the subcommand performs.
	redirects []redirect
	// changesDir marks a subcommand that moves the shell before the rest of the
	// line runs, which leaves later paths relative to an unknown directory.
	changesDir bool
}

// redirect is one output redirection: the path it writes to, when the text
// pins that path down.
type redirect struct {
	target  string
	literal bool // target is a plain word, so it can be resolved as a path
}

// nonLiteralRedirectChars are the metacharacters that survive parsing into a
// single literal word but still do not name one file. Expansions proper —
// `$var`, backticks, quotes — never reach a word literal at all, so they are
// absent here.
const nonLiteralRedirectChars = `*?[]{}~`

// directoryCommands move the shell's working directory.
var directoryCommands = map[string]bool{"cd": true, "pushd": true, "popd": true}

// wrapperCommands are prefixes that leave the command they precede in charge of
// the shell's directory, so `env FOO=1 cd x` still has to be read as a cd.
var wrapperCommands = map[string]bool{
	"command": true, "builtin": true, "exec": true,
	"env": true, "nohup": true, "sudo": true, "time": true,
}

// runsHiddenCode reports whether the node is a construct whose commands do not
// appear in the text as commands: command substitution ($(…) and backticks),
// process substitution (<(…) and >(…)), a subshell, a brace group, and the
// bodies of if / while / for / case / function definitions. Arithmetic and test
// expressions are deliberately absent — they expand words, and a command
// substitution inside one is caught on its own.
func runsHiddenCode(node syntax.Node) bool {
	switch node.(type) {
	case *syntax.CmdSubst, *syntax.ProcSubst,
		*syntax.Subshell, *syntax.Block,
		*syntax.IfClause, *syntax.WhileClause, *syntax.ForClause,
		*syntax.CaseClause, *syntax.FuncDecl:
		return true
	}
	return false
}

func statementRunsHiddenCode(stmt *syntax.Stmt) bool {
	hidden := false
	// The whole statement, not just its command: an unquoted heredoc body and a
	// redirect target are both expanded, so `cat <<EOF … $(cmd) … EOF` runs a
	// command that neither the command node nor the text spells out.
	syntax.Walk(stmt, func(node syntax.Node) bool {
		if node == nil {
			return true
		}
		if runsHiddenCode(node) {
			hidden = true
			return false
		}
		return true
	})
	return hidden
}

// writesToFile reports whether the operator sends output to a path, as opposed
// to reading (`<`) or feeding a here-document.
func writesToFile(op syntax.RedirOperator) bool {
	switch op {
	case syntax.RdrOut, syntax.AppOut, syntax.RdrInOut,
		syntax.ClbOut, syntax.RdrAll, syntax.AppAll:
		return true
	}
	return false
}

// changesDirectory reports whether the command is one of the built-ins that
// move the shell, looking through any safe wrapper and assignment in front of
// it. A command it cannot read through is reported as a plain command: the
// redirections of an unreadable command are only ever asked about anyway.
func changesDirectory(cmd syntax.Command) bool {
	call, ok := cmd.(*syntax.CallExpr)
	if !ok {
		return false
	}
	for _, arg := range call.Args {
		name := arg.Lit()
		if name == "" {
			return false
		}
		if directoryCommands[name] {
			return true
		}
		if wrapperCommands[name] || strings.HasPrefix(name, "-") || strings.Contains(name, "=") {
			continue
		}
		return false
	}
	return false
}

func collectRedirects(stmt *syntax.Stmt) []redirect {
	var redirects []redirect
	for _, redir := range stmt.Redirs {
		if redir.Word == nil {
			continue
		}
		target := redir.Word.Lit()
		switch redir.Op {
		case syntax.DplOut:
			// `2>&1` copies a descriptor; `>&file` writes to a file.
			if isFileDescriptor(target) {
				continue
			}
		default:
			if !writesToFile(redir.Op) {
				continue
			}
		}
		if target == "/dev/null" {
			continue // discards output, writes nowhere
		}
		redirects = append(redirects, redirect{
			target:  target,
			literal: target != "" && !strings.ContainsAny(target, nonLiteralRedirectChars),
		})
	}
	return redirects
}

func isFileDescriptor(lit string) bool {
	if lit == "" {
		return false
	}
	return strings.IndexFunc(lit, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

// statementText slices one statement out of the original command. End covers a
// trailing separator, so the separator's own position wins when there is one.
func statementText(src string, stmt *syntax.Stmt) string {
	end := stmt.End()
	if stmt.Semicolon.IsValid() {
		end = stmt.Semicolon
	}
	start, stop := stmt.Pos().Offset(), end.Offset()
	if start >= stop || stop > uint(len(src)) {
		return ""
	}
	return strings.TrimSpace(src[start:stop])
}

func collectSubcommands(src string, stmt *syntax.Stmt, out *[]subcommand) {
	if binary, ok := stmt.Cmd.(*syntax.BinaryCmd); ok {
		switch binary.Op {
		case syntax.AndStmt, syntax.OrStmt, syntax.Pipe:
			collectSubcommands(src, binary.X, out)
			collectSubcommands(src, binary.Y, out)
			return
		}
	}
	text := statementText(src, stmt)
	if text == "" {
		return
	}
	changesDir := changesDirectory(stmt.Cmd)
	*out = append(*out, subcommand{
		text:       text,
		opaque:     statementRunsHiddenCode(stmt),
		redirects:  collectRedirects(stmt),
		changesDir: changesDir,
	})
}

// analyzeCommand splits a command line into the subcommands the shell will run,
// by the shell's own grammar rather than by scanning for separators: a `|`
// inside quotes is an argument, and `&&` only joins when it is an operator.
// The two spellings of a command substitution are one subcommand, since the
// substitution's output is an argument to the command around it, not a command
// in its own right.
//
// A line the parser rejects comes back whole and opaque — a rule must not be
// allowed to read part of something we could not read at all.
func analyzeCommand(command string) []subcommand {
	// Bash grammar, not the POSIX one: this runs against `sh -c`, and the
	// grammar only decides whether the line parses, never where it splits. A
	// construct we cannot parse is one we can only ask about, so the wider
	// grammar keeps common bash-isms from turning into needless questions.
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		if trimmed := strings.TrimSpace(command); trimmed != "" {
			return []subcommand{{text: trimmed, opaque: true}}
		}
		return nil
	}
	var subcommands []subcommand
	for _, stmt := range file.Stmts {
		collectSubcommands(command, stmt, &subcommands)
	}
	return subcommands
}
