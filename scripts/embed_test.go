package scripts

import (
	"strings"
	"testing"
)

// A thin pool reclaims nothing unless the guest sends TRIM. ext4 and xfs get
// it from the periodic fstrim timer, and a ZFS pool only with autotrim on,
// including a pool an earlier provision created without it.
func TestArtifactsScriptTrimsFreedBlocks(t *testing.T) {
	body, err := Provision.ReadFile("provision/artifacts")
	if err != nil {
		t.Fatalf("read embedded artifacts script: %v", err)
	}

	script := string(body)

	for _, want := range []string{
		"systemctl enable --now fstrim.timer",
		`zpool create -f -o ashift=12 -o autotrim=on "${ZPOOL}" "${DATA_DEVICE}"`,
		`zpool set autotrim=on "${ZPOOL}"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("artifacts script is missing %q", want)
		}
	}
}
