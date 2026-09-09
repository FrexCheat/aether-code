package tui

import (
	"strings"
	"testing"
)

func TestParseCommand(t *testing.T) {
	cases := []struct {
		in   string
		want command
	}{
		{"hello world", cmdNone},
		{"  plain text with /slash inside", cmdNone},
		{"/quit", cmdQuit},
		{"/exit", cmdQuit},
		{"/clear", cmdClear},
		{"  /clear  ", cmdClear},
		{"/foo", cmdUnknown},
		{"/", cmdUnknown},
	}
	for _, c := range cases {
		got, hint := parseCommand(c.in)
		if got != c.want {
			t.Errorf("parseCommand(%q) = %v, want %v", c.in, got, c.want)
		}
		if got == cmdUnknown {
			if !strings.Contains(hint, "Unknown command") || !strings.Contains(hint, "/clear") {
				t.Errorf("hint for %q = %q", c.in, hint)
			}
		} else if hint != "" {
			t.Errorf("hint for %q should be empty, got %q", c.in, hint)
		}
	}
}

func TestRenderBannerNotEmpty(t *testing.T) {
	out := renderBanner("0.1.0-dev", "/tmp/work", 100)
	if !strings.Contains(out, "AetherCode") || !strings.Contains(out, "0.1.0-dev") || !strings.Contains(out, "/tmp/work") {
		t.Errorf("banner missing fields:\n%s", out)
	}
	if lines := strings.Count(out, "\n") + 1; lines < 6 {
		t.Errorf("banner should include 6-line logo, got %d lines", lines)
	}
	narrow := renderBanner("0.1.0-dev", "/tmp/work", 20)
	if strings.Contains(narrow, "▄") {
		t.Error("narrow banner should drop the logo")
	}
}
