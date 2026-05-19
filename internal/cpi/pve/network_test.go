package pve

import (
	"context"
	"testing"
)

// TestDeleteNetwork_BridgeModeIsNoOp guards against the production
// incident where ocfp teardown --all deleted vmbr0/vmbr1 on the PVE host
// because state had recorded them as "managed" networks during a prior
// bootstrap (EnsureBridge adopted them). Bridges are operator infra;
// teardown must never touch them.
//
// The test calls DeleteNetwork with no underlying PVE client wired up —
// if the code attempted the actual delete it would dereference a nil
// pveClient and panic. A successful no-op means the guard short-circuits
// before any HTTP call.
func TestDeleteNetwork_BridgeModeIsNoOp(t *testing.T) {
	t.Parallel()

	client := &Client{
		config: &Config{
			NetworkMode: "bridge",
		},
	}

	netMgr := &NetworkManager{client: client}

	err := netMgr.DeleteNetwork(context.Background(), "vmbr0")
	if err != nil {
		t.Fatalf("DeleteNetwork(vmbr0) in bridge mode = %v, want nil (no-op)", err)
	}

	err = netMgr.DeleteNetwork(context.Background(), "vmbr1")
	if err != nil {
		t.Fatalf("DeleteNetwork(vmbr1) in bridge mode = %v, want nil (no-op)", err)
	}
}
