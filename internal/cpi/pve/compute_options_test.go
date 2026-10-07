package pve

import (
	"context"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// TestGuestOptionParams pins the options every guest ocfp creates on PVE
// carries. The USB tablet pointer is off, because these are headless servers
// reached over SSH and the emulated absolute-pointing device only costs the
// host wakeups. Protection and start-on-boot reflect what the caller asked
// for, and both are always written so a converged guest ends up in the same
// state as a new one.
func TestGuestOptionParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		options        cpi.GuestOptions
		wantProtection int
		wantOnBoot     int
	}{
		{
			name:           "protected guest that starts on boot",
			options:        cpi.GuestOptions{Protected: true, StartOnBoot: true},
			wantProtection: 1,
			wantOnBoot:     1,
		},
		{
			name:           "protected guest that stays off at boot",
			options:        cpi.GuestOptions{Protected: true},
			wantProtection: 1,
			wantOnBoot:     0,
		},
		{
			name:           "unprotected guest that starts on boot",
			options:        cpi.GuestOptions{StartOnBoot: true},
			wantProtection: 0,
			wantOnBoot:     1,
		},
		{name: "unprotected guest that stays off at boot", options: cpi.GuestOptions{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			params := guestOptionParams(tc.options)

			if got := params[pveKeyTablet]; got != 0 {
				t.Errorf("%s = %v, want 0", pveKeyTablet, got)
			}

			if got := params[pveKeyProtection]; got != tc.wantProtection {
				t.Errorf("%s = %v, want %d", pveKeyProtection, got, tc.wantProtection)
			}

			if got := params[pveKeyOnBoot]; got != tc.wantOnBoot {
				t.Errorf("%s = %v, want %d", pveKeyOnBoot, got, tc.wantOnBoot)
			}

			if len(params) != 3 {
				t.Errorf("guest options must write only tablet, protection, and onboot, got %v", params)
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

// TestApplyGuestOptionsStartOnBootFollowsTheRequest drives the create path for
// both answers. A guest created with StartOnBoot comes back after the Proxmox
// node reboots, and one created without it is written as explicitly not
// starting, so a worker cloned from a template that carried onboot never
// autostarts.
func TestApplyGuestOptionsStartOnBootFollowsTheRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		req  *cpi.InstanceRequest
		want int
	}{
		{name: "start on boot requested", req: &cpi.InstanceRequest{StartOnBoot: true}, want: 1},
		{name: "start on boot not requested", req: &cpi.InstanceRequest{}, want: 0},
		{name: "no request at all", req: nil, want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			manager, fake := newOptionsTestManager()

			err := manager.applyGuestOptions(context.Background(), "pve1", 20000, tc.req)
			if err != nil {
				t.Fatalf("applyGuestOptions: %v", err)
			}

			if !fake.wrotePut("/nodes/pve1/qemu/20000/config", pveKeyOnBoot, tc.want) {
				t.Errorf("want onboot = %d in the config PUT, got %v", tc.want, fake.putParams)
			}
		})
	}
}

// TestEnsureGuestOptionsWritesOnBootOnAnExistingGuest covers the converge path
// for a bastion or artifacts VM built before onboot was set. The write is a
// config PUT, so it reaches a running guest without restarting it.
func TestEnsureGuestOptionsWritesOnBootOnAnExistingGuest(t *testing.T) {
	t.Parallel()

	manager, fake := newOptionsTestManager()

	err := manager.EnsureGuestOptions(context.Background(), "20000", cpi.GuestOptions{Protected: true, StartOnBoot: true})
	if err != nil {
		t.Fatalf("EnsureGuestOptions: %v", err)
	}

	if len(fake.putParams) != 1 {
		t.Fatalf("want one config PUT, got %d: %v", len(fake.putParams), fake.putParams)
	}

	if !fake.wrotePut("/nodes/pve1/qemu/20000/config", pveKeyOnBoot, 1) {
		t.Errorf("want onboot = 1 in the config PUT, got %v", fake.putParams)
	}

	if !fake.wrotePut("/nodes/pve1/qemu/20000/config", pveKeyProtection, 1) {
		t.Errorf("want protection = 1 in the config PUT, got %v", fake.putParams)
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
