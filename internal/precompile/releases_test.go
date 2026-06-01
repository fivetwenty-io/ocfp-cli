package precompile

import (
	"strings"
	"testing"
)

func TestBOSHReleasesPinUpstreamCompiled(t *testing.T) {
	rels := BOSHReleases(DefaultStemcell)
	if len(rels) != 2 {
		t.Fatalf("got %d director releases, want 2", len(rels))
	}
	for _, r := range rels {
		if r.UpstreamCompiledURL == "" {
			t.Errorf("%s: expected upstream compiled URL for noble-1.383", r.Name)
		}
		if !strings.Contains(r.UpstreamCompiledURL, "ubuntu-noble-1.383.tgz") {
			t.Errorf("%s: URL %q missing stemcell suffix", r.Name, r.UpstreamCompiledURL)
		}
	}
}

func TestBOSHReleasesNonNobleHasNoUpstream(t *testing.T) {
	rels := BOSHReleases(Stemcell{OS: "ubuntu-jammy", Version: "1.0"})
	for _, r := range rels {
		if r.UpstreamCompiledURL != "" {
			t.Errorf("%s: expected no upstream compiled URL for jammy, got %q", r.Name, r.UpstreamCompiledURL)
		}
	}
}

func TestParseCFReleases(t *testing.T) {
	manifest := []byte(`---
name: cf
releases:
- name: capi
  version: 1.235.0
  url: https://storage.googleapis.com/x/capi.tgz
  sha1: sha256:abc
- name: uaa
  version: 78.14.0
  url: https://storage.googleapis.com/x/uaa.tgz
  sha1: def
stemcells:
- alias: default
  os: ubuntu-noble
  version: "1.364"
`)

	rels, err := ParseCFReleases(manifest, 2)
	if err != nil {
		t.Fatalf("ParseCFReleases: %v", err)
	}
	if len(rels) != 2 {
		t.Fatalf("got %d releases, want 2", len(rels))
	}
	if rels[0].Name != "capi" || rels[0].Version != "1.235.0" {
		t.Errorf("release[0] = %+v", rels[0])
	}
	if rels[0].UpstreamSourceURL == "" || rels[0].UpstreamSourceSHA != "sha256:abc" {
		t.Errorf("release[0] source not parsed: %+v", rels[0])
	}
	// CF has no upstream compiled for our target stemcell.
	if rels[0].UpstreamCompiledURL != "" {
		t.Errorf("release[0] should have no compiled url, got %q", rels[0].UpstreamCompiledURL)
	}
}

func TestParseCFReleasesTruncatedFails(t *testing.T) {
	manifest := []byte("---\nreleases:\n- name: capi\n  version: 1.0\n")
	if _, err := ParseCFReleases(manifest, 30); err == nil {
		t.Error("expected error when fewer than minExpected releases")
	}
}

func TestParseCFReleasesMissingFieldFails(t *testing.T) {
	manifest := []byte("---\nreleases:\n- name: capi\n")
	if _, err := ParseCFReleases(manifest, 1); err == nil {
		t.Error("expected error for release missing version")
	}
}
