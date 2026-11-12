package ssh

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBastionHostKeyOptionsCheckTheRealKnownHosts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := BastionHostKeyOptions()
	want := []string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + filepath.Join(home, ".ssh", "known_hosts"),
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BastionHostKeyOptions() = %q, want %q", got, want)
	}
}

func TestBastionHostKeyOptionsQuoteAPathWithSpaces(t *testing.T) {
	home := filepath.Join(t.TempDir(), "with space")
	t.Setenv("HOME", home)

	got := BastionHostKeyOptions()
	want := `UserKnownHostsFile="` + filepath.Join(home, ".ssh", "known_hosts") + `"`

	if len(got) != 4 || got[3] != want {
		t.Fatalf("BastionHostKeyOptions() = %q, want the last option %q", got, want)
	}
}

// Without a home directory the file is still named on the command line, as
// the literal ~/.ssh/known_hosts that ssh expands itself, so a user's ssh
// config cannot point the check at /dev/null.
func TestBastionHostKeyOptionsWithoutAHomeStillNameTheFile(t *testing.T) {
	t.Setenv("HOME", "")

	got := BastionHostKeyOptions()
	want := []string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=~/.ssh/known_hosts",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BastionHostKeyOptions() = %q, want %q", got, want)
	}
}

func TestInternalHostKeyOptionsStayRelaxed(t *testing.T) {
	t.Parallel()

	got := InternalHostKeyOptions()
	want := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=" + os.DevNull,
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("InternalHostKeyOptions() = %q, want %q", got, want)
	}
}

func TestShellJoinQuotesOnlyWhatAShellWouldSplit(t *testing.T) {
	t.Parallel()

	got := ShellJoin([]string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", `UserKnownHostsFile="/home/o p/.ssh/known_hosts"`,
		"-o", "UserKnownHostsFile=/home/it's/known_hosts",
		"-W", "%h:%p", "ubuntu@10.0.0.4",
	})
	want := `-o StrictHostKeyChecking=accept-new` +
		` -o 'UserKnownHostsFile="/home/o p/.ssh/known_hosts"'` +
		` -o 'UserKnownHostsFile=/home/it'\''s/known_hosts'` +
		` -W %h:%p ubuntu@10.0.0.4`

	if got != want {
		t.Fatalf("ShellJoin():\n got %s\nwant %s", got, want)
	}
}
