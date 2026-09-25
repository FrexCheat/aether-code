package tools

import "testing"

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		rel     string
		want    bool
	}{
		// Patterns without ** take filepath.Match's semantics.
		{"literal", "a/b.go", "a/b.go", true},
		{"star within a segment", "*.go", "main.go", true},
		{"star does not cross a separator", "*.go", "a/main.go", false},
		// ** crosses any number of segments, including zero.
		{"double star zero segments", "a/**/c.go", "a/c.go", true},
		{"double star crosses", "a/**/c.go", "a/b/x/c.go", true},
		{"leading double star", "**/*.go", "x/y/z.go", true},
		{"trailing double star", "a/**", "a/b/c.go", true},
		{"trailing double star matches the parent", "a/**", "a", true},
		{"mid-pattern double star", "a/**/b/*.go", "a/x/y/b/main.go", true},
		{"mid-pattern double star mismatch", "a/**/b/*.go", "a/x/y/main.go", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchGlob(tt.pattern, tt.rel); got != tt.want {
				t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pattern, tt.rel, got, tt.want)
			}
		})
	}
}
