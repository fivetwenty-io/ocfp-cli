package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

func TestListBlocNames_SingleBloc(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")

	yml := []byte("blocs:\n  mybloc:\n    name: mybloc\n    provider: stackit\n")

	err := os.WriteFile(cfgPath, yml, 0o600)
	if err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	names, err := config.ListBlocNames(cfgPath)
	if err != nil {
		t.Fatalf("ListBlocNames failed: %v", err)
	}

	if len(names) != 1 || names[0] != "mybloc" {
		t.Fatalf("expected [mybloc], got %v", names)
	}
}

func TestListBlocNames_MultipleBlocs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")

	yml := []byte("blocs:\n  charlie:\n    name: charlie\n  alpha:\n    name: alpha\n  bravo:\n    name: bravo\n")

	err := os.WriteFile(cfgPath, yml, 0o600)
	if err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	names, err := config.ListBlocNames(cfgPath)
	if err != nil {
		t.Fatalf("ListBlocNames failed: %v", err)
	}

	expected := []string{"alpha", "bravo", "charlie"}
	if len(names) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, names)
	}

	for i, name := range names {
		if name != expected[i] {
			t.Fatalf("expected %v at index %d, got %v", expected[i], i, name)
		}
	}
}

func TestListBlocNames_MissingFile(t *testing.T) {
	t.Parallel()

	names, err := config.ListBlocNames("/nonexistent/path/config.yml")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}

	if len(names) != 0 {
		t.Fatalf("expected empty slice, got %v", names)
	}
}

func TestListBlocNames_EmptyBlocs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")

	yml := []byte("blocs:\n")

	err := os.WriteFile(cfgPath, yml, 0o600)
	if err != nil {
		t.Fatalf("failed to write temp config: %v", err)
	}

	names, err := config.ListBlocNames(cfgPath)
	if err != nil {
		t.Fatalf("ListBlocNames failed: %v", err)
	}

	if len(names) != 0 {
		t.Fatalf("expected empty slice, got %v", names)
	}
}
