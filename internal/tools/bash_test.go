package tools

import "testing"

func TestLastCommandName(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{"plain", "ls -la", "ls"},
		{"pipeline tail", "cat x | grep foo", "grep"},
		{"quoted pipe is an argument", `echo "a|b"`, "echo"},
		{"and-join resolves to the tail", "git status && ls", "ls"},
		{"pipeline after a join", "a && b | c", "c"},
		{"absolute path", "/usr/bin/awk '{print}' x", "awk"},
		{"wrappers are not peeled", "timeout 10 grep foo", "timeout"},
		{"unparsable falls back to the split", `echo "unclosed`, "echo"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastCommandName(tt.command); got != tt.want {
				t.Errorf("lastCommandName(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}
