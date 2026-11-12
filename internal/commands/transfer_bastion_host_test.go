package commands

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	bastionssh "github.com/fivetwenty-io/ocfp-cli/internal/bastion/ssh"
	"github.com/fivetwenty-io/ocfp-cli/internal/logger"
)

func TestTransferRemoteHost(t *testing.T) {
	t.Parallel()

	for path, want := range map[string]string{
		"ubuntu@10.64.64.20:/tmp/f":     "10.64.64.20",
		"10.64.64.20:f":                 "10.64.64.20",
		"ubuntu@[fd00::10]:/tmp/f":      "fd00::10",
		"artifacts.internal:/srv":       "artifacts.internal",
		"host::module/path":             "host",
		"scp://ubuntu@10.0.0.4:2222/f":  "10.0.0.4",
		"rsync://[fd00::10]:873/module": "fd00::10",
		"/local/file":                   "",
		"./dir/with:colon":              "",
		"relative":                      "",
		"":                              "",
	} {
		if got := transferRemoteHost(path); got != want {
			t.Errorf("transferRemoteHost(%q) = %q, want %q", path, got, want)
		}
	}
}

// aliasRefusal prints what ssh prints when it refuses host's key from an
// entry in our own known_hosts. For a name rather than an address, that
// alone would pass for the bastion's refusal under a host alias.
func aliasRefusal(home, host string) string {
	return "echo 'Offending ED25519 key in " + filepath.Join(home, ".ssh", "known_hosts") + ":1' >&2\n" +
		"echo 'Host key for " + host + " has changed and you have requested strict checking.' >&2\n" +
		"echo 'Host key verification failed.' >&2\n" +
		"exit 255\n"
}

// scp and rsync check every remote end strictly, so a refusal can come
// from a host other than the bastion. It is blamed on the bastion only
// when one end of the copy is the bastion.
func TestTransfersBlameTheBastionOnlyForItsOwnEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	const bastionIP = "100.109.226.53"

	runs := []struct {
		name        string
		tool        string
		destination string
		wantBastion bool
		run         func(destination string) error
	}{
		{"scp to the bastion", "scp", "bastion:/tmp/", true, func(destination string) error {
			return transferSCP(context.Background(), &scpConfig{User: "ubuntu", Source: "f", Destination: destination}, bastionIP, "") //nolint:exhaustruct // only the paths matter
		}},
		{"scp to an internal host", "scp", "ubuntu@artifacts.internal:/tmp/", false, func(destination string) error {
			return transferSCP(context.Background(), &scpConfig{User: "ubuntu", Source: "f", Destination: destination}, bastionIP, "") //nolint:exhaustruct // only the paths matter
		}},
		{"rsync to the bastion", "rsync", "bastion:/tmp/", true, func(destination string) error {
			return executeRSyncWithEnvironment(context.Background(), logger.WithOperation("test"),
				&rsyncConfig{user: "ubuntu", source: "f", destination: destination}, //nolint:exhaustruct // only the paths matter
				&rsyncEnvironment{bastionIP: bastionIP})                             //nolint:exhaustruct // only the bastion matters
		}},
		{"rsync to an internal host", "rsync", "ubuntu@artifacts.internal:/tmp/", false, func(destination string) error {
			return executeRSyncWithEnvironment(context.Background(), logger.WithOperation("test"),
				&rsyncConfig{user: "ubuntu", source: "f", destination: destination}, //nolint:exhaustruct // only the paths matter
				&rsyncEnvironment{bastionIP: bastionIP})                             //nolint:exhaustruct // only the bastion matters
		}},
	}

	for _, tt := range runs {
		refused := "artifacts.internal"
		if tt.wantBastion {
			refused = bastionIP
		}

		fakeOnPath(t, tt.tool, aliasRefusal(home, refused))

		err := tt.run(tt.destination)
		if err == nil {
			t.Errorf("%s: got no error, want the refusal", tt.name)

			continue
		}

		if got := errors.Is(err, bastionssh.ErrHostKeyMismatch); got != tt.wantBastion {
			t.Errorf("%s: blamed on the bastion = %v, want %v (%v)", tt.name, got, tt.wantBastion, err)
		}
	}
}
