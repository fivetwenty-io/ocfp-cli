package commands

import (
	"context"
	"net/http"
	"time"
)

// vaultHealthCheckTimeout bounds the seal-status probe. The vault is local, so
// a healthy one answers in milliseconds.
const vaultHealthCheckTimeout = 3 * time.Second

// vaultRespondsOnAddr reports whether a vault is serving at addr.
//
// This asks the vault's own API rather than shelling out to `vault status`.
// The CLI reads ~/.vault as its token-helper config, but safe uses that path
// for its metadata DIRECTORY, so on any workstation with safe installed
// `vault status` exits non-zero with "failed to get token helper: ... is a
// directory" before it ever contacts the server.
//
// That mattered because this check guards cleanupExistingVault: a false
// negative sends ensureInceptionVault past its idempotency fast-path and into
// os.RemoveAll of the vault directory, taking root.key and unseal.keys with
// it. A liveness check must not depend on unrelated local CLI configuration.
//
// Any HTTP response counts as alive, including a sealed vault (503) or a
// permission error. The question is whether something is serving this port and
// therefore must not be deleted — not whether we can read from it.
func vaultRespondsOnAddr(ctx context.Context, addr string) bool {
	ctx, cancel := context.WithTimeout(ctx, vaultHealthCheckTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr+"/v1/sys/seal-status", nil)
	if err != nil {
		return false
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}

	defer func() { _ = resp.Body.Close() }()

	return true
}
