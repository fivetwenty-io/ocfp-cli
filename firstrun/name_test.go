package firstrun

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// closedName is the name of the managing system, spelled from its code
// points so that this file never holds it.
func closedName() string {
	return string([]rune{0x63, 0x6f, 0x73, 0x6d, 0x6f, 0x73})
}

// ownedOutside lists the files outside this package that carry the
// first-run key's code, relative to this package's directory. Each one
// must exist, so the list can't drift from the tree.
var ownedOutside = []string{
	"../internal/vault/firstrun.go",
	"../internal/vault/firstrun_test.go",
	"../internal/vault/policy.go",
	"../internal/vault/policy_firstrun_test.go",
	"../internal/commands/firstrun.go",
	"../internal/commands/firstrun_test.go",
	"../internal/commands/firstrun_cmd_test.go",
	"../internal/commands/vault.go",
	"../internal/bastion/firstrun_summary.go",
	"../internal/bastion/firstrun_summary_test.go",
}

// TestFilesNeverNameTheManagingSystem searches every file of this
// package, its test data included, and every file in ownedOutside, for
// the managing system's name. This code is public, so it uses generic
// terms only.
func TestFilesNeverNameTheManagingSystem(t *testing.T) {
	t.Parallel()

	name := closedName()

	check := func(path string) error {
		if strings.Contains(strings.ToLower(path), name) {
			t.Errorf("the path %s holds the name", path)
		}

		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		if strings.Contains(strings.ToLower(string(b)), name) {
			t.Errorf("%s holds the name", path)
		}

		return nil
	}

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		return check(path)
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range ownedOutside {
		err := check(filepath.FromSlash(path))
		if err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}
