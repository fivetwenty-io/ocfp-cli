package precompile

import (
	"strings"
	"testing"
)

func TestRenderCompileManifest(t *testing.T) {
	rels := []Release{{Name: "capi", Version: "1.235.0"}, {Name: "uaa", Version: "78.14.0"}}
	out, err := RenderCompileManifest("dev-precompile-cf", rels, DefaultStemcell)
	if err != nil {
		t.Fatalf("RenderCompileManifest: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		"name: dev-precompile-cf",
		"instance_groups: []",
		"- name: capi",
		"- name: uaa",
		"os: ubuntu-noble",
		`version: "1.383"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("manifest missing %q\n---\n%s", want, s)
		}
	}
}

func TestRenderCompileManifestRejectsEmpty(t *testing.T) {
	if _, err := RenderCompileManifest("", nil, DefaultStemcell); err == nil {
		t.Error("expected error for empty name")
	}
	if _, err := RenderCompileManifest("x", nil, DefaultStemcell); err == nil {
		t.Error("expected error for no releases")
	}
}
