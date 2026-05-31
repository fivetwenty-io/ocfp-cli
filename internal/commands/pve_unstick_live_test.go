//go:build live

package commands

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLive_UnstickAgent_SSHReturnsActive exercises the full unstickAgent path
// against a real PVE host.
//
// Required env vars:
//
//	OCFP_PVE_HOST         — PVE host IP or FQDN (e.g. "192.168.1.10")
//	OCFP_PVE_TEST_VMID    — numeric VMID of the test VM (e.g. "101")
//
// Optional:
//
//	OCFP_SSH_UNSAFE=1     — skip host-key checking (useful on lab hosts)
//
// Run with:
//
//	go test -tags live -run TestLive_UnstickAgent_SSHReturnsActive ./internal/commands/
func TestLive_UnstickAgent_SSHReturnsActive(t *testing.T) {
	pveHost := os.Getenv("OCFP_PVE_HOST")
	if pveHost == "" {
		t.Skip("OCFP_PVE_HOST not set; skipping live test")
	}

	rawVMID := os.Getenv("OCFP_PVE_TEST_VMID")
	if rawVMID == "" {
		t.Skip("OCFP_PVE_TEST_VMID not set; skipping live test")
	}

	vmid, err := coerceVMID(rawVMID)
	require.NoError(t, err, "OCFP_PVE_TEST_VMID must be a positive integer")
	assert.Greater(t, vmid, 0)

	sshUnsafe := os.Getenv("OCFP_SSH_UNSAFE") == "1"

	err = unstickAgent(pveHost, vmid, sshUnsafe)
	assert.NoError(t, err, "unstickAgent must succeed against live PVE host %s vmid %d", pveHost, vmid)
}
