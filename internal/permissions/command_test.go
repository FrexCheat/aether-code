package permissions

import "testing"

func TestAnalyzeCommand(t *testing.T) {
	tests := []struct {
		name      string
		command   string
		wantSubs  int
		wantShown []string // subcommand texts, in order
		opaque    []bool
	}{
		{
			name: "plain command", command: "ls -la",
			wantSubs: 1, wantShown: []string{"ls -la"}, opaque: []bool{false},
		},
		{
			name: "pipe joins two commands", command: "ls | grep foo",
			wantSubs: 2, wantShown: []string{"ls", "grep foo"}, opaque: []bool{false, false},
		},
		{
			name: "quoted pipe is an argument", command: `echo "a|b"`,
			wantSubs: 1, wantShown: []string{`echo "a|b"`}, opaque: []bool{false},
		},
		{
			name: "and-join", command: "git status && ls",
			wantSubs: 2, wantShown: []string{"git status", "ls"}, opaque: []bool{false, false},
		},
		{
			name: "substitution is opaque", command: "echo $(rm x)",
			wantSubs: 1, wantShown: []string{"echo $(rm x)"}, opaque: []bool{true},
		},
		{
			name: "control flow body is opaque", command: "if true; then ls; fi",
			wantSubs: 1, wantShown: []string{"if true; then ls; fi"}, opaque: []bool{true},
		},
		{
			name: "unparsable line is one opaque piece", command: `echo "unclosed`,
			wantSubs: 1, wantShown: []string{`echo "unclosed`}, opaque: []bool{true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subs := analyzeCommand(tt.command)
			if len(subs) != tt.wantSubs {
				t.Fatalf("got %d subcommands, want %d: %+v", len(subs), tt.wantSubs, subs)
			}
			for i, want := range tt.wantShown {
				if subs[i].text != want {
					t.Errorf("subcommand[%d] = %q, want %q", i, subs[i].text, want)
				}
				if subs[i].opaque != tt.opaque[i] {
					t.Errorf("subcommand[%d].opaque = %v, want %v", i, subs[i].opaque, tt.opaque[i])
				}
			}
		})
	}
}

func TestAnalyzeCommandRedirectsAndCd(t *testing.T) {
	subs := analyzeCommand("ls > out.txt")
	if len(subs) != 1 || len(subs[0].redirects) != 1 {
		t.Fatalf("redirect not collected: %+v", subs)
	}
	if r := subs[0].redirects[0]; r.target != "out.txt" || !r.literal {
		t.Errorf("redirect = %+v, want literal out.txt", r)
	}

	subs = analyzeCommand(`ls > "$F"`)
	if len(subs) != 1 || len(subs[0].redirects) != 1 || subs[0].redirects[0].literal {
		t.Errorf("quoted target must not be literal: %+v", subs)
	}

	subs = analyzeCommand("cd /tmp && ls")
	if len(subs) != 2 || !subs[0].changesDir || subs[1].changesDir {
		t.Errorf("cd must be marked changesDir: %+v", subs)
	}
}
