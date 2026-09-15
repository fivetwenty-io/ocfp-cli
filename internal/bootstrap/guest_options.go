package bootstrap

import (
	"context"
	"fmt"
	"os"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// guestOptioner is the provider capability that converges a VM's
// provider-level options. Only PVE implements it, so the call site asks for
// it rather than widening cpi.ComputeManager for every provider.
type guestOptioner interface {
	EnsureGuestOptions(ctx context.Context, instanceID string, protected bool) error
}

// ensureGuestOptions asks the provider to converge the guest options on a VM
// ocfp owns, and says nothing when the provider has no such notion.
//
// Every bloc built before these options existed has a bastion with a live
// pointer device and no deletion guard, and the provider's web UI is the only
// other way to change that. Running this on each bootstrap turns a re-run into
// the fix. A failure is reported and does not stop the run: the VM works
// either way, and refusing to finish a bootstrap over a flag would be worse
// than leaving it for the next pass.
func ensureGuestOptions(ctx context.Context, compute interface{}, instanceID, name string) {
	optioner, ok := compute.(guestOptioner)
	if !ok {
		return
	}

	err := optioner.EnsureGuestOptions(ctx, instanceID, true)
	if err != nil {
		logger.Warnf("Could not converge the guest options on %s (%s): %v", name, instanceID, err)

		return
	}

	logger.Debugf("Converged guest options on %s (%s)", name, instanceID)
}

// EnsureBastionGuestOptions converges the bastion's provider-level options.
//
// It is a bootstrap step of its own for the same reason the data disk is:
// CreateBastion returns early whenever a bastion already exists, which is the
// ordinary case on every re-run, so anything bolted onto the end of that
// function would never reach the bastions that need it most.
func (m *Manager) EnsureBastionGuestOptions(ctx context.Context) error {
	compute := m.provider.ComputeManager()
	if compute == nil {
		return nil
	}

	bastionName := m.options.BlocName + "-bastion"

	instanceID, ok := m.bastionInstanceID(bastionName)
	if !ok {
		logger.Debugf("No bastion recorded for %s; skipping guest options", bastionName)

		return nil
	}

	if _, supported := compute.(guestOptioner); !supported {
		return nil
	}

	ensureGuestOptions(ctx, compute, instanceID, bastionName)

	_, _ = fmt.Fprintf(os.Stdout, "    • Bastion %s guest options converged\n", bastionName)

	return nil
}
