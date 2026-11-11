package providers

import (
	"slices"
	"testing"
)

// The bastion SSH client checks host keys against the user's known_hosts.
// No provider may hand it options that switch that check off.
func TestBastionSSHOptionsKeepHostKeyChecks(t *testing.T) {
	t.Parallel()

	cfg := newPVEConfig()

	sets := map[string][]string{
		"aws":     NewAWSBastionInit(cfg).sshOptions(),
		"gcp":     NewGCPBastionInit(cfg).getSSHOptions(),
		"pve":     NewPVEBastionInit(cfg).buildSSHOptions(),
		"stackit": NewStackitBastionInit(cfg).buildSSHOptions(),
	}

	for provider, options := range sets {
		for _, banned := range []string{"StrictHostKeyChecking=no", "UserKnownHostsFile=/dev/null"} {
			if slices.Contains(options, banned) {
				t.Errorf("%s bastion ssh options %v contain %q", provider, options, banned)
			}
		}
	}
}
