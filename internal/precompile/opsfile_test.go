package precompile

import (
	"strings"
	"testing"
)

func TestRenderOpsFileRoundTrip(t *testing.T) {
	sc := DefaultStemcell
	rels := []Resolution{
		{Release: Release{Name: "capi", Version: "1.235.0"}, Source: SourceCompiled,
			URL: "https://10.0.0.5:9000/dev-ocf-bosh/compiled-releases/capi-1.235.0-ubuntu-noble-1.383.tgz",
			SHA: "sha256:deadbeef"},
	}

	out, err := RenderOpsFile(rels, sc, "ocfp precompile cf")
	if err != nil {
		t.Fatalf("RenderOpsFile: %v", err)
	}
	s := string(out)

	for _, want := range []string{
		"# stemcell: ubuntu-noble/1.383",
		"path: /releases/name=capi?",
		`sha1: "sha256:deadbeef"`,
		"os: ubuntu-noble",
		`version: "1.383"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("ops file missing %q\n---\n%s", want, s)
		}
	}

	// The stemcell marker must round-trip back out.
	got, ok := StemcellFromOps(out)
	if !ok {
		t.Fatal("StemcellFromOps: marker not found")
	}
	if got != sc {
		t.Errorf("StemcellFromOps = %v, want %v", got, sc)
	}
}

func TestRenderOpsFileRejectsEmpty(t *testing.T) {
	if _, err := RenderOpsFile(nil, DefaultStemcell, "x"); err == nil {
		t.Error("expected error for empty resolutions")
	}
}

func TestRenderOpsFileRejectsMissingSHA(t *testing.T) {
	rels := []Resolution{{Release: Release{Name: "a", Version: "1"}, URL: "https://x/a.tgz"}}
	if _, err := RenderOpsFile(rels, DefaultStemcell, "x"); err == nil {
		t.Error("expected error for missing sha")
	}
}

func TestStemcellFromOpsAbsent(t *testing.T) {
	if _, ok := StemcellFromOps([]byte("---\nreleases: []\n")); ok {
		t.Error("expected ok=false when no marker present")
	}
}
