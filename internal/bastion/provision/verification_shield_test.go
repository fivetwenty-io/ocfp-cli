package provision

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// The SHIELD CLI is verified, but only in the optional group. A bastion with no
// SHIELD core deployed is still a working bastion, so a missing shield binary
// must not fail provisioning the way a missing bosh or safe does.
func TestVerification_ShieldIsOptionalNotRequired(t *testing.T) {
	cfg := &config.Config{}
	vm := NewVerificationManager("pve", cfg)

	var (
		foundOptional bool
		foundRequired bool
	)

	for _, verification := range vm.GetToolVerifications() {
		for _, command := range verification.Commands {
			if command != "shield" {
				continue
			}

			if verification.Required {
				foundRequired = true
			} else {
				foundOptional = true
			}
		}
	}

	if !foundOptional {
		t.Error("expected shield among the optional tool verifications")
	}

	if foundRequired {
		t.Error("expected shield to stay out of the required verifications: provisioning must not fail without it")
	}
}
