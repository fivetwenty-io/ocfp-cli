//go:build live
// +build live

package pve

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// TestLive_Termproxy_Smoke drives a real PVE serial console via termproxy to
// verify the WebSocket auth handshake, the size-prefixed wire format, and the
// expect-style helpers all interoperate with PVE.
//
// Gated on TERMPROXY_LIVE_VMID — when unset the test no-ops. Run manually
// after pre-provisioning a test VM with ciuser=ubuntu cipassword=termtest:
//
//	TERMPROXY_LIVE_VMID=999 \
//	  TERMPROXY_LIVE_ENDPOINT=https://pve.example.com:8006 \
//	  TERMPROXY_LIVE_TOKEN='root@pam!foo=...' \
//	  TERMPROXY_LIVE_NODE=pve \
//	  go test ./internal/cpi/pve/ -run TestLive_Termproxy_Smoke -v -count=1
func TestLive_Termproxy_Smoke(t *testing.T) {
	vmidStr := os.Getenv("TERMPROXY_LIVE_VMID")
	if vmidStr == "" {
		t.Skip("set TERMPROXY_LIVE_VMID to run live smoke")
	}

	vmid, err := strconv.Atoi(vmidStr)
	if err != nil {
		t.Fatalf("TERMPROXY_LIVE_VMID must be numeric: %v", err)
	}

	endpoint := envOrDefault("TERMPROXY_LIVE_ENDPOINT", "")
	token := envOrDefault("TERMPROXY_LIVE_TOKEN", "")
	node := envOrDefault("TERMPROXY_LIVE_NODE", "pve")

	if endpoint == "" || token == "" {
		t.Fatal("TERMPROXY_LIVE_ENDPOINT and TERMPROXY_LIVE_TOKEN required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	sess, err := OpenTermproxy(ctx, endpoint, "PVEAPIToken="+token, node, vmid, false)
	if err != nil {
		t.Fatalf("OpenTermproxy: %v", err)
	}

	defer sess.Close()

	// Multiple CRs because the VM might be mid-boot or already past the
	// first prompt; each CR re-triggers the getty.
	for i := 0; i < 3; i++ {
		if err := sess.SendLine(""); err != nil {
			t.Fatalf("send wake CR: %v", err)
		}

		time.Sleep(500 * time.Millisecond)
	}

	loginRe := regexp.MustCompile(`login:\s*$`)
	if _, err := sess.ExpectRegex(loginRe, 60*time.Second); err != nil {
		t.Fatalf("login prompt: %v", err)
	}

	_ = sess.SendLine("ubuntu")

	pwRe := regexp.MustCompile(`Password:\s*$`)
	if _, err := sess.ExpectRegex(pwRe, 10*time.Second); err != nil {
		t.Fatalf("password prompt: %v", err)
	}

	_ = sess.SendLine("termtest")

	shellRe := regexp.MustCompile(`\$\s*$`)
	if _, err := sess.ExpectRegex(shellRe, 20*time.Second); err != nil {
		t.Fatalf("shell prompt: %v", err)
	}

	_ = sess.SendLine("echo TERMPROXY_GO_OK_$(uname -r)")

	markerRe := regexp.MustCompile(`TERMPROXY_GO_OK_\S+`)
	out, err := sess.ExpectRegex(markerRe, 10*time.Second)
	if err != nil {
		t.Fatalf("marker round-trip: %v", err)
	}

	t.Logf("verified marker: %s", markerRe.FindString(out))
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return def
}
