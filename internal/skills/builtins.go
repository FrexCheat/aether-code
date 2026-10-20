package skills

import "embed"

// builtinFS holds the samples that ship with the binary. They have no
// directory, so they are never part of the hot-reload fingerprint: the tier
// cannot change while the program runs.
//
//go:embed builtins/*.md
var builtinFS embed.FS

// scanBuiltins reads the embedded tier. Every entry is a single file, and a
// file that fails to parse is a build-time mistake rather than a user's — it
// still only costs itself, reported like any other skip.
func scanBuiltins() ([]*Skill, []string) {
	entries, err := builtinFS.ReadDir("builtins")
	if err != nil {
		return nil, []string{"skills: cannot read the embedded tier: " + err.Error()}
	}
	root := "builtins"
	var skills []*Skill
	var notes []string
	for _, entry := range entries {
		path := root + "/" + entry.Name()
		data, err := builtinFS.ReadFile(path)
		if err != nil {
			notes = append(notes, "skills: "+path+": "+err.Error())
			continue
		}
		skill, _, err := Parse(path, SourceBuiltin, data)
		if err != nil {
			notes = append(notes, "skills: "+path+": "+err.Error())
			continue
		}
		skills = append(skills, skill)
	}
	return skills, notes
}
