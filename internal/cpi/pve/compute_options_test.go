package pve

import (
	"context"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// TestGuestOptionParams pins the two options every guest ocfp creates on PVE
// carries: the USB tablet pointer is off, because these are headless servers
// reached over SSH and the emulated absolute-pointing device only costs the
// host wakeups, and protection reflects what the caller asked for.
func TestGuestOptionParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		protected      bool
		wantProtection int
	}{
		{name: "protected guest", protected: true, wantProtection: 1},
		{name: "unprotected guest", protected: false, wantProtection: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			params := guestOptionParams(tc.protected)

			if got := params[pveKeyTablet]; got != 0 {
				t.Errorf("%s = %v, want 0", pveKeyTablet, got)
			}

			if got := params[pveKeyProtection]; got != tc.wantProtection {
				t.Errorf("%s = %v, want %d", pveKeyProtection, got, tc.wantProtection)
			}

			if len(params) != 2 {
				t.Errorf("guest options must write only tablet and protection, got %v", params)
			}
		})
	}
}

// newOptionsTestManager wires a ComputeManager over a fake API pinned to one
// node, so a test can read back exactly the config writes PVE would receive.
func newOptionsTestManager() (*ComputeManager, *fakePVEClient) {
	fake := &fakePVEClient{}

	client := &Client{
		config:    &Config{Node: "pve1", DefaultStorage: "local-lvm"},
		pveClient: fake,
	}

	return &ComputeManager{client: client}, fake
}

// TestApplyGuestOptionsWritesBothKeys drives the config write a freshly
// created guest gets before it is started.
func TestApplyGuestOptionsWritesBothKeys(t *testing.T) {
	t.Parallel()

	manager, fake := newOptionsTestManager()

	err := manager.applyGuestOptions(context.Background(), "pve1", 20000, &cpi.InstanceRequest{Protected: true})
	if err != nil {
		t.Fatalf("applyGuestOptions: %v", err)
	}

	if len(fake.putParams) != 1 {
		t.Fatalf("want one config PUT, got %d: %v", len(fake.putParams), fake.putParams)
	}

	call := fake.putParams[0]

	if call.Path != "/nodes/pve1/qemu/20000/config" {
		t.Errorf("PUT path = %q, want /nodes/pve1/qemu/20000/config", call.Path)
	}

	if got := call.Params[pveKeyTablet]; got != 0 {
		t.Errorf("tablet = %v, want 0", got)
	}

	if got := call.Params[pveKeyProtection]; got != 1 {
		t.Errorf("protection = %v, want 1", got)
	}
}

// TestSetProtectionConvergesAnExistingGuest covers the path that fixes a
// bastion or artifacts VM built before protection was set at creation. It is
// the only route short of the PVE web UI, so it has to work on a guest the
// current run did not create.
func TestSetProtectionConvergesAnExistingGuest(t *testing.T) {
	t.Parallel()

	manager, fake := newOptionsTestManager()

	err := manager.SetProtection(context.Background(), "20000", true)
	if err != nil {
		t.Fatalf("SetProtection: %v", err)
	}

	if len(fake.putParams) != 1 {
		t.Fatalf("want one config PUT, got %d: %v", len(fake.putParams), fake.putParams)
	}

	call := fake.putParams[0]

	if call.Path != "/nodes/pve1/qemu/20000/config" {
		t.Errorf("PUT path = %q, want /nodes/pve1/qemu/20000/config", call.Path)
	}

	if got := call.Params[pveKeyProtection]; got != 1 {
		t.Errorf("protection = %v, want 1", got)
	}

	if _, wrote := call.Params[pveKeyTablet]; wrote {
		t.Errorf("SetProtection must write only protection, got %v", call.Params)
	}
}

// TestDeleteInstanceClearsProtectionFirst is the reason protection is safe to
// set at all. PVE's destroy refuses a protected guest outright
// ("can't remove VM <vmid>"), so every teardown, every rollback of a
// half-built artifacts VM, and the destroy at the end of a recycle would fail
// if the flag were left on.
func TestDeleteInstanceClearsProtectionFirst(t *testing.T) {
	t.Parallel()

	manager, fake := newOptionsTestManager()

	err := manager.DeleteInstance(context.Background(), "20000")
	if err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}

	if !fake.wrotePut("/nodes/pve1/qemu/20000/config", pveKeyProtection, 0) {
		t.Errorf("delete must clear protection before destroying the guest, PUTs: %v", fake.putParams)
	}

	if fake.deleteCalls != 1 {
		t.Errorf("delete calls = %d, want 1", fake.deleteCalls)
	}
}
