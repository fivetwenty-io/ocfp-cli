package netlayout_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/netlayout"
)

// writeStrategyFile writes a minimal, valid strategy definition (one mgmt
// static, one closed band, no ocf tier so the haproxy/CF-kit coupling never
// triggers) to dir/name.yaml with the given strategy name and scheme
// version, and returns its path.
func writeStrategyFile(t *testing.T, dir, filename, name, schemeVersion string) string {
	t.Helper()

	content := "name: " + name + "\n" +
		"description: test BYO strategy\n" +
		"scheme_version: \"" + schemeVersion + "\"\n" +
		"placement: colocated\n" +
		"min_prefix: 26\n" +
		"\n" +
		"tiers:\n" +
		"  mgmt:\n" +
		"    statics:\n" +
		"      bosh: 4\n" +
		"    available:\n" +
		"      - start: 10\n" +
		"        end: 20\n"

	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write strategy file %s: %v", path, err)
	}

	return path
}

func TestBuildCatalog(t *testing.T) {
	t.Parallel()

	t.Run("ValidBYOFileIsLookupableAndListed", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		writeStrategyFile(t, dir, "byo1.yaml", "byo1", "byo1-v1")

		cat, err := netlayout.BuildCatalog([]string{filepath.Join(dir, "byo1.yaml")}, dir)
		if err != nil {
			t.Fatalf("BuildCatalog: unexpected error: %v", err)
		}

		layout, err := cat.Lookup("byo1")
		if err != nil {
			t.Fatalf("Lookup(%q): unexpected error: %v", "byo1", err)
		}

		if got := layout.Name(); got != "byo1" {
			t.Fatalf("Lookup(%q).Name() = %q, want %q", "byo1", got, "byo1")
		}

		found := false

		for _, n := range cat.Names() {
			if n == "byo1" {
				found = true
			}
		}

		if !found {
			t.Fatalf("Names() = %v, want it to include %q", cat.Names(), "byo1")
		}

		// Built-ins remain present alongside the BYO addition.
		if _, err := cat.Lookup("wide"); err != nil {
			t.Fatalf("Lookup(%q): unexpected error: %v", "wide", err)
		}
	})

	t.Run("BYONamedWideShadowsBuiltinAndErrors", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		writeStrategyFile(t, dir, "wide.yaml", "wide", "shadow-v1")

		_, err := netlayout.BuildCatalog([]string{filepath.Join(dir, "wide.yaml")}, dir)
		if err == nil {
			t.Fatal("BuildCatalog: want error for BYO strategy shadowing built-in name \"wide\", got nil")
		}

		if !errors.Is(err, netlayout.ErrStrategyShadowed) {
			t.Fatalf("BuildCatalog error = %v, want wrapping ErrStrategyShadowed", err)
		}

		if !strings.Contains(err.Error(), "wide") {
			t.Fatalf("BuildCatalog error %q does not mention the shadowed name", err.Error())
		}
	})

	t.Run("TwoBYOFilesSharingSchemeVersionError", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		writeStrategyFile(t, dir, "byo-a.yaml", "byo-a", "shared-scheme")
		writeStrategyFile(t, dir, "byo-b.yaml", "byo-b", "shared-scheme")

		_, err := netlayout.BuildCatalog(
			[]string{filepath.Join(dir, "byo-a.yaml"), filepath.Join(dir, "byo-b.yaml")}, dir)
		if err == nil {
			t.Fatal("BuildCatalog: want error for two BYO strategies sharing scheme_version, got nil")
		}

		if !errors.Is(err, netlayout.ErrSchemeCollision) {
			t.Fatalf("BuildCatalog error = %v, want wrapping ErrSchemeCollision", err)
		}
	})

	t.Run("BYOReusingBuiltinSchemeVersionErrors", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		// "2" is wide's own scheme_version.
		writeStrategyFile(t, dir, "byo-c.yaml", "byo-c", "2")

		_, err := netlayout.BuildCatalog([]string{filepath.Join(dir, "byo-c.yaml")}, dir)
		if err == nil {
			t.Fatal("BuildCatalog: want error for BYO strategy reusing wide's scheme_version \"2\", got nil")
		}

		if !errors.Is(err, netlayout.ErrSchemeCollision) {
			t.Fatalf("BuildCatalog error = %v, want wrapping ErrSchemeCollision", err)
		}
	})

	t.Run("DirectoryLoadsEveryYAMLFileSorted", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		writeStrategyFile(t, dir, "aaa.yml", "dir-first", "dir-first-v1")
		writeStrategyFile(t, dir, "zzz.yaml", "dir-second", "dir-second-v1")
		// A non-YAML file in the same directory must be ignored, not loaded
		// as a (failing) strategy definition.
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("not yaml"), 0o600); err != nil {
			t.Fatalf("write README.md: %v", err)
		}

		cat, err := netlayout.BuildCatalog([]string{dir}, "")
		if err != nil {
			t.Fatalf("BuildCatalog: unexpected error: %v", err)
		}

		for _, name := range []string{"dir-first", "dir-second"} {
			if _, err := cat.Lookup(name); err != nil {
				t.Errorf("Lookup(%q): unexpected error: %v", name, err)
			}
		}
	})

	t.Run("RelativePathResolvesAgainstBaseDir", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		writeStrategyFile(t, dir, "relative.yaml", "relative-strategy", "relative-v1")

		cat, err := netlayout.BuildCatalog([]string{"relative.yaml"}, dir)
		if err != nil {
			t.Fatalf("BuildCatalog: unexpected error: %v", err)
		}

		if _, err := cat.Lookup("relative-strategy"); err != nil {
			t.Fatalf("Lookup(%q): unexpected error: %v", "relative-strategy", err)
		}
	})

	t.Run("UnreadablePathErrorsNamingPath", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		missing := filepath.Join(dir, "does-not-exist.yaml")

		_, err := netlayout.BuildCatalog([]string{missing}, dir)
		if err == nil {
			t.Fatal("BuildCatalog: want error for unreadable strategy path, got nil")
		}

		if !strings.Contains(err.Error(), missing) {
			t.Fatalf("BuildCatalog error %q does not name the offending path %q", err.Error(), missing)
		}
	})
}

func TestBuiltins(t *testing.T) {
	t.Parallel()

	t.Run("FreshCatalogEachCall", func(t *testing.T) {
		t.Parallel()

		a := netlayout.Builtins()
		b := netlayout.Builtins()

		wideA, err := a.Lookup("wide")
		if err != nil {
			t.Fatalf("Lookup(%q): unexpected error: %v", "wide", err)
		}

		wideB, err := b.Lookup("wide")
		if err != nil {
			t.Fatalf("Lookup(%q): unexpected error: %v", "wide", err)
		}

		if wideA.Name() != wideB.Name() {
			t.Fatalf("Builtins() instances disagree on wide's name: %q vs %q", wideA.Name(), wideB.Name())
		}
	})

	t.Run("EmptyNameIsAnError", func(t *testing.T) {
		t.Parallel()

		if _, err := netlayout.Builtins().Lookup(""); err == nil {
			t.Fatal("Catalog.Lookup(\"\") want error (callers resolve defaults first), got nil")
		}
	})
}

func TestDefaultNameFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		provider       string
		subnetStrategy string
		want           string
	}{
		{"pve", "", "wide"},
		{"aws", "", "spanning"},
		{"stackit", "ocfp-single", "wide"},
		{"stackit", "ocfp-triple", "spanning"},
		{"", "", "wide"},
		// config.go validates provider with strings.EqualFold and never
		// normalizes the stored value, so a config carrying "AWS" or
		// "Stackit" reaches this function verbatim and must resolve to
		// the same default its lowercase spelling does.
		{"AWS", "", "spanning"},
		{"Aws", "", "spanning"},
		{"Stackit", "ocfp-triple", "spanning"},
		{"STACKIT", "ocfp-single", "wide"},
		{"PVE", "", "wide"},
	}

	for _, tt := range tests {
		got := netlayout.DefaultNameFor(tt.provider, tt.subnetStrategy)
		if got != tt.want {
			t.Errorf("DefaultNameFor(%q, %q) = %q, want %q", tt.provider, tt.subnetStrategy, got, tt.want)
		}
	}
}
