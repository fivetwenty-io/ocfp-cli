package commands

import (
	"errors"
	"strings"
	"testing"
)

// rsync and scp run whatever program the last -e, --rsh, or -S names, so
// an extra option that names one would replace the ssh command that checks
// the bastion's host key. Those options are refused.
func TestRSyncRefusesOptionsThatReplaceTheSSHCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	refused := []struct {
		name        string
		options     string
		passthrough []string
	}{
		{name: "ShortOption", options: "-e ssh"},
		{name: "ShortOptionAttached", options: "-essh"},
		{name: "ShortOptionBundled", options: "-avze ssh"},
		{name: "LongOption", options: "--rsh ssh"},
		{name: "LongOptionWithValue", options: "--rsh=ssh"},
		{name: "PassthroughShortOption", passthrough: []string{"-e", "ssh -o StrictHostKeyChecking=no"}},
		{name: "PassthroughLongOption", passthrough: []string{"--rsh=ssh"}},
	}

	for _, tt := range refused {
		config := &rsyncConfig{rsyncOptions: tt.options, passthrough: tt.passthrough} //nolint:exhaustruct // only the extra options matter

		_, err := buildRSyncCommand(config, "a/", "ubuntu@10.61.148.10:b/", "")
		if !errors.Is(err, ErrRSyncShellOption) {
			t.Errorf("%s: got %v, want ErrRSyncShellOption", tt.name, err)
		}

		if err != nil && !strings.Contains(err.Error(), "host key") {
			t.Errorf("%s: error %q does not say why", tt.name, err)
		}
	}

	allowed := []struct {
		name        string
		options     string
		passthrough []string
	}{
		{name: "OrdinaryOptions", options: "-avz --partial"},
		{name: "FilterHoldingAnE", options: "-fexclude=*.tmp"},
		{name: "LongOptionsWithAnE", options: "--exclude=e --delete-excluded"},
		{name: "PassthroughOrdinary", passthrough: []string{"--itemize-changes", "--copy-links"}},
	}

	for _, tt := range allowed {
		config := &rsyncConfig{rsyncOptions: tt.options, passthrough: tt.passthrough} //nolint:exhaustruct // only the extra options matter

		_, err := buildRSyncCommand(config, "a/", "ubuntu@10.61.148.10:b/", "")
		if err != nil {
			t.Errorf("%s: got %v, want no error", tt.name, err)
		}
	}
}

func TestSCPRefusesAnOptionThatReplacesSSH(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	for _, options := range []string{"-S /tmp/myssh", "-S/tmp/myssh", "-rS /tmp/myssh", "-C -S ssh"} {
		cfg := &scpConfig{Source: "f", Destination: "bastion:/tmp/f", Options: options} //nolint:exhaustruct // only the paths and options matter

		_, err := prepareSCPCommand(cfg, "10.61.148.10", "")
		if !errors.Is(err, ErrSCPProgramOption) {
			t.Errorf("%q: got %v, want ErrSCPProgramOption", options, err)
		}
	}

	for _, options := range []string{"-C", "-l 8192", "-o Compression=yes", "-P 2222", "-iS"} {
		cfg := &scpConfig{Source: "f", Destination: "bastion:/tmp/f", Options: options} //nolint:exhaustruct // only the paths and options matter

		_, err := prepareSCPCommand(cfg, "10.61.148.10", "")
		if err != nil {
			t.Errorf("%q: got %v, want no error", options, err)
		}
	}
}
