package sigchain

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

// TestPackageNeverNamesTheManagingSystem searches every file of the
// package, its test data included, for the managing system's name. This
// package is public, so it uses generic terms only.
func TestPackageNeverNamesTheManagingSystem(t *testing.T) {
	t.Parallel()

	name := closedName()

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

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
	})
	if err != nil {
		t.Fatal(err)
	}
}
