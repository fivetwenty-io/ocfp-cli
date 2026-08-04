package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestXDGConfigHome_EnvVarUsedVerbatim(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	want := filepath.Join(tmp, "ocfp")
	if got := configHome(); got != want {
		t.Errorf("configHome() = %q, want %q", got, want)
	}
}

func TestXDGConfigHome_DefaultLiteralPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	want := filepath.Join(home, ".config", "ocfp")
	if got := configHome(); got != want {
		t.Errorf("configHome() = %q, want %q (must be literal, no GOOS branching)", got, want)
	}
}

func TestXDGStateHome_EnvVarUsedVerbatim(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)

	want := filepath.Join(tmp, "ocfp")
	if got := stateHome(); got != want {
		t.Errorf("stateHome() = %q, want %q", got, want)
	}
}

func TestXDGStateHome_DefaultLiteralPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	want := filepath.Join(home, ".local", "state", "ocfp")
	if got := stateHome(); got != want {
		t.Errorf("stateHome() = %q, want %q (must be literal, no GOOS branching)", got, want)
	}
}

func TestXDGDataHome_EnvVarUsedVerbatim(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmp)

	want := filepath.Join(tmp, "ocfp")
	if got := dataHome(); got != want {
		t.Errorf("dataHome() = %q, want %q", got, want)
	}
}

func TestXDGDataHome_DefaultLiteralPath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	want := filepath.Join(home, ".local", "share", "ocfp")
	if got := dataHome(); got != want {
		t.Errorf("dataHome() = %q, want %q (must be literal, no GOOS branching)", got, want)
	}
}

func TestXDGResolveExisting_NewPathExists(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "new", "config.yml")
	legacyPath := filepath.Join(dir, "legacy", "config.yml")
	mustWriteFile(t, newPath)
	mustWriteFile(t, legacyPath)

	resetLegacyWarnOnce()
	got, usedLegacy := resolveExisting(newPath, legacyPath)
	if got != newPath {
		t.Errorf("resolveExisting() path = %q, want newPath %q", got, newPath)
	}
	if usedLegacy {
		t.Errorf("resolveExisting() usedLegacy = true, want false when newPath exists")
	}
}

func TestXDGResolveExisting_OnlyLegacyExists(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "new", "config.yml")
	legacyPath := filepath.Join(dir, "legacy", "config.yml")
	mustWriteFile(t, legacyPath)

	resetLegacyWarnOnce()
	got, usedLegacy := resolveExisting(newPath, legacyPath)
	if got != legacyPath {
		t.Errorf("resolveExisting() path = %q, want legacyPath %q", got, legacyPath)
	}
	if !usedLegacy {
		t.Errorf("resolveExisting() usedLegacy = false, want true when only legacyPath exists")
	}
}

func TestXDGResolveExisting_NeitherExists(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "new", "config.yml")
	legacyPath := filepath.Join(dir, "legacy", "config.yml")

	resetLegacyWarnOnce()
	got, usedLegacy := resolveExisting(newPath, legacyPath)
	if got != newPath {
		t.Errorf("resolveExisting() path = %q, want newPath %q (write target) when neither exists", got, newPath)
	}
	if usedLegacy {
		t.Errorf("resolveExisting() usedLegacy = true, want false when neither path exists")
	}
}

func TestXDGResolveExisting_WarnsOnceAcrossMultipleCalls(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "legacy", "config.yml")
	mustWriteFile(t, legacyPath)

	resetLegacyWarnOnce()

	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error: %v", err)
	}
	os.Stderr = w

	// Two different new-path/legacy-path pairs, both falling back to
	// legacy: exactly one warning must print for the whole
	// process, not one per call.
	newPathA := filepath.Join(dir, "new-a", "config.yml")
	newPathB := filepath.Join(dir, "new-b", "config.yml")
	resolveExisting(newPathA, legacyPath)
	resolveExisting(newPathB, legacyPath)

	w.Close()
	os.Stderr = origStderr

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("failed to read captured stderr: %v", err)
	}

	got := buf.String()
	count := bytesCount(got, legacyPath)
	if count != 1 {
		t.Errorf("expected exactly one legacy-path warning across process, got %d occurrences in output:\n%s", count, got)
	}
}

func TestLegacyHome_IgnoresOCFPHomeOverride(t *testing.T) {
	override := t.TempDir()
	t.Setenv("OCFP_HOME", override)

	home := t.TempDir()
	t.Setenv("HOME", home)

	want := filepath.Join(home, ".ocfp")
	if got := LegacyHome(); got != want {
		t.Errorf("LegacyHome() = %q, want %q (must ignore OCFP_HOME, unlike OcfpHome())", got, want)
	}
}

func TestLegacyHome_MatchesOcfpHomeFallback(t *testing.T) {
	t.Setenv("OCFP_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	want := filepath.Join(home, ".ocfp")
	if got := LegacyHome(); got != want {
		t.Errorf("LegacyHome() = %q, want %q", got, want)
	}

	if got := OcfpHome(); got != want {
		t.Errorf("OcfpHome() = %q, want %q to match LegacyHome() when OCFP_HOME is unset", got, want)
	}
}

// mustWriteFile creates path's parent directories and an empty file at path.
func mustWriteFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error: %v", path, err)
	}
}

// resetLegacyWarnOnce resets the package-level sync.Once guarding the
// legacy-fallback warning so each test observes its own warn-once window.
func resetLegacyWarnOnce() {
	legacyWarnOnce = sync.Once{}
}

// bytesCount counts non-overlapping occurrences of substr in s.
func bytesCount(s, substr string) int {
	return strings.Count(s, substr)
}
