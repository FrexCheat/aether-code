package skills

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		data     string
		wantName string
		wantDir  string
		wantDesc string
		wantBody string
		wantErr  string
	}{
		{
			name:     "full frontmatter",
			path:     "/skills/report.md",
			data:     "---\nname: report\ndescription: 生成周报\n---\n\n正文第一行。\n\n第二段。\n",
			wantName: "report",
			wantDesc: "生成周报",
			wantBody: "正文第一行。\n\n第二段。\n",
		},
		{
			name:     "no frontmatter falls back to the file name and the first plain line",
			path:     "/skills/changelog.md",
			data:     "\n# 标题\n\n按标签汇总改动。\n\n细节。\n",
			wantName: "changelog",
			wantDesc: "按标签汇总改动。",
			wantBody: "# 标题\n\n按标签汇总改动。\n\n细节。\n",
		},
		{
			name:     "bundle entry takes the directory name",
			path:     "/skills/report/SKILL.md",
			data:     "---\ndescription: 一份周报\n---\n正文\n",
			wantName: "report",
			wantDir:  "/skills/report",
			wantDesc: "一份周报",
			wantBody: "正文\n",
		},
		{
			name:     "frontmatter name wins over the file name",
			path:     "/skills/x.md",
			data:     "---\nname: real\n---\n正文\n",
			wantName: "real",
			wantDesc: "正文",
			wantBody: "正文\n",
		},
		{
			name:     "CRLF frontmatter and body",
			path:     "/skills/x.md",
			data:     "---\r\nname: x\r\n---\r\n\r\n正文\r\n",
			wantName: "x",
			wantDesc: "正文",
			wantBody: "正文\n",
		},
		{
			name:     "body keeps its first line's indentation",
			path:     "/skills/x.md",
			data:     "---\n---\n  缩进的正文\n",
			wantName: "x",
			wantDesc: "缩进的正文",
			wantBody: "  缩进的正文\n",
		},
		{
			name:     "empty body is not a parse error",
			path:     "/skills/x.md",
			data:     "---\nname: x\n---\n",
			wantName: "x",
			wantBody: "",
		},
		{
			name:    "name with whitespace is rejected",
			path:    "/skills/x.md",
			data:    "---\nname: two words\n---\n正文\n",
			wantErr: "whitespace",
		},
		{
			name:    "leading slash in the name is rejected",
			path:    "/skills/x.md",
			data:    "---\nname: /x\n---\n正文\n",
			wantErr: "must not start with /",
		},
		{
			name:    "broken YAML is a parse failure",
			path:    "/skills/x.md",
			data:    "---\nname: [unclosed\n---\n正文\n",
			wantErr: "frontmatter",
		},
		{
			name:    "an unclosed fence is a parse failure",
			path:    "/skills/x.md",
			data:    "---\nname: x\n正文\n",
			wantErr: "never closed",
		},
		{
			name:     "unknown fields are ignored",
			path:     "/skills/x.md",
			data:     "---\nname: x\nmodel: opus\nallowed_tools: [Bash]\n---\n正文\n",
			wantName: "x",
			wantDesc: "正文",
			wantBody: "正文\n",
		},
		{
			name:     "retired mode and history fields are ignored",
			path:     "/skills/x.md",
			data:     "---\nname: x\nmode: isolated\nhistory: 8\n---\n正文\n",
			wantName: "x",
			wantDesc: "正文",
			wantBody: "正文\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skill, body, err := Parse(tt.path, SourceProject, []byte(tt.data))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Parse() error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() = %v", err)
			}
			if skill.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", skill.Name, tt.wantName)
			}
			if skill.Dir != tt.wantDir {
				t.Errorf("Dir = %q, want %q", skill.Dir, tt.wantDir)
			}
			if skill.Description != tt.wantDesc {
				t.Errorf("Description = %q, want %q", skill.Description, tt.wantDesc)
			}
			if skill.Source != SourceProject {
				t.Errorf("Source = %q, want the one it was parsed as", skill.Source)
			}
			if body != tt.wantBody {
				t.Errorf("body = %q, want %q", body, tt.wantBody)
			}
		})
	}
}

func TestDescribeIsOneLineAndCapped(t *testing.T) {
	long := strings.Repeat("描", descriptionMax+10)
	got := describe("", long+"\n")
	if runes := []rune(got); len(runes) != descriptionMax+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("describe() kept %d runes, want %d and an ellipsis", len(runes), descriptionMax+1)
	}
	if got := describe("one\n  two", "ignored"); got != "one two" {
		t.Errorf("describe() = %q, want the declared line flattened", got)
	}
}
