package tools

import (
	"slices"
	"testing"
)

func TestBuiltinManifestMatchesRegistry(t *testing.T) {
	registry := NewBuiltinRegistry("", nil)
	names := registry.Names()
	specs := BuiltinSpecs()

	if len(names) != len(specs) {
		t.Fatalf("registry has %d tools, the manifest %d: %v vs %v", len(names), len(specs), names, BuiltinSpecs())
	}
	manifest := map[string]Spec{}
	for _, spec := range specs {
		manifest[spec.Name] = spec.Spec
		if spec.Spec.RuleArg == "" {
			t.Errorf("%s: RuleArg must address the tool's rule argument", spec.Name)
		}
		if spec.Spec.PromptLine == "" {
			t.Errorf("%s: PromptLine must carry the prompt bullet", spec.Name)
		}
	}
	for _, name := range names {
		if _, ok := manifest[name]; !ok {
			t.Errorf("%s is registered but missing from the manifest", name)
		}
		tool := registry.Get(name)
		if _, ok := tool.(Specified); !ok {
			t.Errorf("%s does not implement Specified", name)
		}
	}
	if !slices.Contains(BuiltinNames(), "Bash") {
		t.Errorf("BuiltinNames misses Bash: %v", BuiltinNames())
	}
}
