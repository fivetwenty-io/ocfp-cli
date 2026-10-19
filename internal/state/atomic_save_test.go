package state_test

import (
	"bytes"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveBackupFailurePreservesLiveState(t *testing.T) {
	dir := t.TempDir()
	m, err := state.NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	current, err := m.Load("bloc")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Save(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "bloc.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(path+".backup", 0700); err != nil {
		t.Fatal(err)
	}
	current.Provider = "changed"
	if err = m.Save(); err == nil {
		t.Fatal("save accepted failed backup")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed save altered live state")
	}
}
func TestSavePreservesPriorBytesAndPrivateModes(t *testing.T) {
	dir := t.TempDir()
	m, err := state.NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	current, err := m.Load("bloc")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Save(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "bloc.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	current.Provider = "changed"
	if err = m.Save(); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + ".backup")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, backup) {
		t.Fatal("backup differs from previous state")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("unexpected temporary files: %v", entries)
	}
	for _, name := range []string{path, path + ".backup"} {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("unsafe mode %s", info.Mode())
		}
	}
}
