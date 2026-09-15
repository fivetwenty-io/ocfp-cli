package pve

import (
	"context"
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// PVE config keys for the two guest options ocfp manages.
const (
	// pveKeyTablet is the emulated USB tablet pointer. It exists so a
	// graphical console can use absolute pointing, which a headless server
	// reached over SSH never does, and it costs the host a wakeup per
	// pointer event. Every guest ocfp creates turns it off.
	pveKeyTablet = "tablet"

	// pveKeyProtection is Proxmox's guard against removing a guest or its
	// disks. See PVE::QemuConfig->check_protection in the API: it refuses
	// the destroy and both halves of a detach, and nothing else.
	pveKeyProtection = "protection"
)

// guestOptionParams builds the config write that carries ocfp's guest options.
//
// Both keys are always written, so the resulting state is the same whether the
// guest is new or is being converged: an unprotected guest is explicitly
// unprotected rather than left at whatever the template happened to carry.
func guestOptionParams(protected bool) map[string]interface{} {
	protection := 0
	if protected {
		protection = 1
	}

	return map[string]interface{}{
		pveKeyTablet:     0,
		pveKeyProtection: protection,
	}
}

// applyGuestOptions writes the guest options for a freshly created VM.
//
// This runs after the disks are in place and before the guest is started.
// Proxmox gates removals on the protection flag, never additions, so a disk
// attached after this point still attaches; see EnsureGuestOptions for the
// operations that have to drop the flag first.
func (m *ComputeManager) applyGuestOptions(ctx context.Context, node string, vmid int, req *cpi.InstanceRequest) error {
	protected := req != nil && req.Protected

	return m.writeGuestConfig(ctx, node, vmid, guestOptionParams(protected))
}

// EnsureGuestOptions converges the guest options on a VM that already exists.
//
// The bastion and artifacts VMs of every bloc built before these options
// existed carry the Proxmox defaults, a live pointer device and no protection,
// and the web UI is otherwise the only way to change them. Bootstrap calls
// this on each run so a re-run fixes them.
func (m *ComputeManager) EnsureGuestOptions(ctx context.Context, instanceID string, protected bool) error {
	vmid, err := parseVMID(instanceID)
	if err != nil {
		return err
	}

	node, err := m.findVMNode(ctx, vmid)
	if err != nil {
		return err
	}

	return m.writeGuestConfig(ctx, node, vmid, guestOptionParams(protected))
}

// SetProtection turns the protection flag on or off, leaving every other
// option alone.
//
// Turning it off is what makes protection safe to set: the destroy and the
// detach both have to drop it first, and they must not disturb anything else
// about a guest they may yet leave in place.
func (m *ComputeManager) SetProtection(ctx context.Context, instanceID string, enabled bool) error {
	vmid, err := parseVMID(instanceID)
	if err != nil {
		return err
	}

	node, err := m.findVMNode(ctx, vmid)
	if err != nil {
		return err
	}

	return m.writeGuestConfig(ctx, node, vmid, protectionParams(enabled))
}

// protectionParams is the config write that toggles protection alone.
func protectionParams(enabled bool) map[string]interface{} {
	value := 0
	if enabled {
		value = 1
	}

	return map[string]interface{}{pveKeyProtection: value}
}

// writeGuestConfig PUTs a guest's config keys.
func (m *ComputeManager) writeGuestConfig(ctx context.Context, node string, vmid int, params map[string]interface{}) error {
	path := buildPVEPathf(node, "qemu/%d/config", vmid)

	_, err := m.client.pveClient.PutCtx(ctx, path, params)
	if err != nil {
		return fmt.Errorf("write VM %d config %v: %w", vmid, params, err)
	}

	return nil
}

// clearProtection drops the protection flag so a removal can proceed.
//
// Proxmox refuses to destroy a protected guest or to remove a drive from one,
// and both of those are things ocfp does on purpose: teardown destroys the
// guest, and the preserve path detaches the data disk first precisely so the
// destroy cannot take it along. A failure here is logged rather than returned,
// because the operation that follows reports the refusal with the context an
// operator needs, and a guest that was not protected in the first place must
// not fail on a flag it never had.
func clearProtection(ctx context.Context, client *Client, node string, vmid int) {
	path := buildPVEPathf(node, "qemu/%d/config", vmid)

	_, err := client.pveClient.PutCtx(ctx, path, protectionParams(false))
	if err != nil {
		logger.Warnf("Could not clear the protection flag on VM %d: %v", vmid, err)
	}
}
