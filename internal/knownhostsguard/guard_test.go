package knownhostsguard

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuardPassesWhenTheFileIsUnchanged(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "known_hosts")

	err := os.WriteFile(path, []byte("host ssh-ed25519 AAAA\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	var report bytes.Buffer

	if code := guard(path, &report, func() int { return 0 }); code != 0 || report.Len() != 0 {
		t.Errorf("got code %d and report %q, want 0 and nothing", code, report.String())
	}
}

func TestGuardFailsWhenTheTestsChangeTheFile(t *testing.T) {
	t.Parallel()

	runs := map[string]struct {
		before *string
		after  string
	}{
		"Appended": {before: ptr("host ssh-ed25519 AAAA\n"), after: "host ssh-ed25519 AAAA\n[127.0.0.1]:61596 ssh-ed25519 BBBB\n"},
		"Created":  {before: nil, after: "[127.0.0.1]:61596 ssh-ed25519 BBBB\n"},
	}

	for name, run := range runs {
		path := filepath.Join(t.TempDir(), "known_hosts")

		if run.before != nil {
			err := os.WriteFile(path, []byte(*run.before), 0o600)
			if err != nil {
				t.Fatal(err)
			}
		}

		var report bytes.Buffer

		code := guard(path, &report, func() int {
			_ = os.WriteFile(path, []byte(run.after), 0o600)

			return 0
		})
		if code == 0 {
			t.Errorf("%s: got code 0, want a failure", name)
		}

		if !strings.Contains(report.String(), path) {
			t.Errorf("%s: report %q does not name %s", name, report.String(), path)
		}
	}
}

func TestGuardKeepsTheTestsFailure(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "known_hosts")

	var report bytes.Buffer

	if code := guard(path, &report, func() int { return 3 }); code != 3 {
		t.Errorf("got code %d, want the tests' own 3", code)
	}
}

func ptr(s string) *string { return &s }
