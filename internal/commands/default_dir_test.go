package commands

import (
	"os"
	"path/filepath"
	"testing"
)

// inside compares directories by identity, so two spellings of one
// directory count as the same directory on any file system.
func TestInsideComparesDirectoriesByIdentity(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	owner := filepath.Join(base, "owner")
	jobs := filepath.Join(owner, "jobs")

	for _, dir := range []string{owner, jobs, filepath.Join(base, "owner-dev")} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	// dotted names the same directory as owner by a different string.
	dotted := owner + string(filepath.Separator) + "."

	cases := []struct {
		name string
		a, b located
		want bool
	}{
		{"one directory by two strings", located{near: owner}, located{near: dotted}, true},
		{"a directory under the other spelling", located{near: jobs}, located{near: dotted}, true},
		{"a missing name under the other spelling", located{near: dotted, missing: []string{"spool"}}, located{near: owner}, true},
		{"a missing target reached through the other spelling", located{near: dotted, missing: []string{"Owner", "jobs"}},
			located{near: base, missing: []string{"owner"}}, true},
		{"the parent", located{near: base}, located{near: dotted}, false},
		{"a sibling whose name starts the same way", located{near: filepath.Join(base, "owner-dev")}, located{near: dotted}, false},
		{"a missing sibling", located{near: base, missing: []string{"owner-jobs"}}, located{near: base, missing: []string{"owner"}}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := inside(tc.a, tc.b)
			if err != nil || got != tc.want {
				t.Fatalf("inside(%v, %v) = %v, %v; want %v", tc.a, tc.b, got, err, tc.want)
			}
		})
	}
}

// namesStartWith compares names that do not exist yet without regard to
// case or Unicode normalization, so a doubtful case counts as a match.
func TestNamesStartWith(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		names, prefix []string
		want          bool
	}{
		{"the same names", []string{"data", "owner"}, []string{"data", "owner"}, true},
		{"a longer path", []string{"data", "owner", "jobs"}, []string{"data", "owner"}, true},
		{"no missing names", []string{"jobs"}, nil, true},
		{"a different case", []string{"DATA", "Owner"}, []string{"data", "owner"}, true},
		{"a decomposed accent", []string{"café"}, []string{"café"}, true},
		{"a decomposed accent in another case", []string{"CAFÉ"}, []string{"café"}, true},
		{"a shorter path", []string{"data"}, []string{"data", "owner"}, false},
		{"a different name", []string{"data", "owner-dev"}, []string{"data", "owner"}, false},
		{"a different accent", []string{"cafe"}, []string{"café"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := namesStartWith(tc.names, tc.prefix); got != tc.want {
				t.Fatalf("namesStartWith(%q, %q) = %v, want %v", tc.names, tc.prefix, got, tc.want)
			}
		})
	}
}
