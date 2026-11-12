package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/fivetwenty-io/ocfp-cli/internal/state"
)

// An operator who types the bastion's address or host name instead of
// "bastion" still reaches the bastion, so its host key is checked.
func TestSSHAddressIsBastion(t *testing.T) {
	t.Parallel()

	known := []string{"203.0.113.7", "10.61.148.10", "fd00::10"}

	named := &config.Config{ //nolint:exhaustruct // only the bastion fields matter
		Name:      "b",
		BastionIP: "100.109.226.53",
		Tailscale: &config.TailscaleConfig{Hostname: "Lab-Jump"}, //nolint:exhaustruct // only the host name matters
	}
	dotted := &config.Config{ //nolint:exhaustruct // only the bastion fields matter
		Name:      "b",
		Tailscale: &config.TailscaleConfig{Hostname: "jump.lab.example.org"}, //nolint:exhaustruct // only the host name matters
	}
	unnamed := &config.Config{Name: "b"} //nolint:exhaustruct // only the bloc name matters

	tests := []struct {
		name    string
		address string
		cfg     *config.Config
		known   []string
		want    bool
	}{
		{"ConfiguredBastionIP", "100.109.226.53", named, nil, true},
		{"RecordedPublicIPBesideAnOverride", "203.0.113.7", named, known, true},
		{"RecordedPrivateIPBesideAnOverride", "10.61.148.10", named, known, true},
		{"RecordedIPv6", "[FD00::0:10]", unnamed, known, true},
		{"TailscaleHostName", "lab-jump", named, nil, true},
		{"TailscaleHostNameAnyCase", "LAB-JUMP", named, nil, true},
		{"MagicDNSName", "lab-jump.tail1234.ts.net.", named, nil, true},
		{"DefaultHostName", "B-Bastion.tail1234.ts.net", unnamed, nil, true},
		{"DottedTailscaleHostName", "jump.lab.example.org", dotted, nil, true},
		{"DottedTailscaleHostNameWithASuffix", "jump.lab.example.org.ts.net", dotted, nil, true},
		{"OtherAddress", "10.64.64.20", named, known, false},
		{"OtherName", "lab-jump-2.tail1234.ts.net", named, known, false},
		{"NameSharingAPrefix", "jump.lab.example.organic", dotted, nil, false},
		{"NothingRecorded", "10.61.148.10", unnamed, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sshAddressIsBastion(tt.address, tt.cfg, tt.known)
			if got != tt.want {
				t.Errorf("sshAddressIsBastion(%q) = %v, want %v", tt.address, got, tt.want)
			}
		})
	}
}

// recordedBastionAddresses reads the addresses bootstrap recorded, and
// only reads: no probe, no provider, and nothing written to the state.
func TestRecordedBastionAddressesOnlyReadsState(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	if got := recordedBastionAddresses("b"); len(got) != 0 {
		t.Errorf("recordedBastionAddresses() = %v with no state, want none", got)
	}

	stateDir, err := state.GetStateDir("b")
	if err != nil {
		t.Fatal(err)
	}

	if _, statErr := os.Stat(stateDir); !os.IsNotExist(statErr) {
		t.Fatalf("reading created the state directory %s", stateDir)
	}

	manager, err := state.NewManager(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = manager.Load("b"); err != nil {
		t.Fatal(err)
	}

	for key, value := range map[string]string{
		"bastion_ssh_host":                "100.109.226.53",
		"bastion_public_ip":               "203.0.113.7",
		"bastion_private_ip":              "10.61.148.10",
		"reserved_b-ocfp-0_bastion_ip":    "10.61.148.9",
		"reserved_b_bastion_ip":           "10.61.148.8",
		"artifacts_private_ip":            "10.61.148.11",
		"bastion_ssh_command_not_an_addr": "ssh ubuntu@100.109.226.53",
	} {
		if err = manager.SetOutput(key, value); err != nil {
			t.Fatal(err)
		}
	}

	if err = manager.Save(); err != nil {
		t.Fatal(err)
	}

	before := stateFiles(t, stateDir)

	got := recordedBastionAddresses("b")
	want := []string{"100.109.226.53", "203.0.113.7", "10.61.148.10", "10.61.148.9", "10.61.148.8"}

	if len(got) != len(want) {
		t.Fatalf("recordedBastionAddresses() = %v, want %v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recordedBastionAddresses() = %v, want %v", got, want)
		}
	}

	if after := stateFiles(t, stateDir); after != before {
		t.Errorf("reading changed the state:\nbefore %s\nafter  %s", before, after)
	}
}

// stateFiles describes every file under dir by name, size, and time.
func stateFiles(t *testing.T, dir string) string {
	t.Helper()

	var out string

	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		out += fmt.Sprintf("%s %s %d\n", path, info.ModTime(), info.Size())

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return out
}

// The address check runs on every address target, so it must answer from
// what is already on disk. It never asks the provider for the bastion.
func TestSSHAddressMatcherAsksNoProvider(t *testing.T) {
	t.Setenv("OCFP_HOME", t.TempDir())

	cfg := &config.Config{Name: "b", BastionIP: "100.109.226.53"} //nolint:exhaustruct // only the bastion fields matter

	isBastion := sshAddressMatcher(cfg)

	if !isBastion(context.Background(), "100.109.226.53") {
		t.Error("the configured bastion address was not recognised")
	}

	if isBastion(context.Background(), "10.64.64.20") {
		t.Error("an internal address was taken for the bastion")
	}
}
