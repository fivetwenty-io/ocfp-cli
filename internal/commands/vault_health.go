package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"syscall"
	"time"
)

const (
	// vaultHealthCheckTimeout bounds the seal-status probe. The vault is
	// local, so a healthy one answers in milliseconds.
	vaultHealthCheckTimeout = 3 * time.Second

	// vaultProbeBodyLimit caps how much of a seal-status answer is read. A
	// real one is a few hundred bytes; a stranger's page need not be read
	// in full to know it is not a vault.
	vaultProbeBodyLimit = 64 << 10
)

// vaultProbeState is what answered on an inception vault's API port.
type vaultProbeState int

const (
	// vaultProbeStopped means nothing is listening on the port.
	vaultProbeStopped vaultProbeState = iota
	// vaultProbeVault means a vault answered seal-status.
	vaultProbeVault
	// vaultProbeStranger means something holds the port but did not answer
	// as a vault, or did not answer in time. Nothing on disk may change
	// while the port is in that state.
	vaultProbeStranger
)

// vaultProbe is the answer to one seal-status request.
type vaultProbe struct {
	state       vaultProbeState
	initialized bool
	sealed      bool
	// storageType is the backend the vault reports, such as "raft" or
	// "file". It is empty when the engine does not report one, and the
	// caller then names the storage from the data directory instead.
	storageType string
}

// sealStatusResponse holds the seal-status fields the probe reads. The two
// booleans are pointers so that JSON lacking them is told apart from a
// vault reporting false.
type sealStatusResponse struct {
	Initialized *bool  `json:"initialized"`
	Sealed      *bool  `json:"sealed"`
	StorageType string `json:"storage_type"`
}

// probeInceptionVault asks addr for /v1/sys/seal-status and reports what is
// there: nothing, a vault with its seal and storage state, or a stranger.
//
// Only a refused connection counts as stopped. A listener that answers with
// something other than seal-status JSON, or that does not answer within
// vaultHealthCheckTimeout, is a stranger: we cannot start a vault on that
// port, and we must not assume the vault behind it is gone.
func probeInceptionVault(ctx context.Context, addr string) vaultProbe {
	ctx, cancel := context.WithTimeout(ctx, vaultHealthCheckTimeout)
	defer cancel()

	stranger := vaultProbe{state: vaultProbeStranger}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr+"/v1/sys/seal-status", nil)
	if err != nil {
		return stranger
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			return vaultProbe{state: vaultProbeStopped}
		}

		return stranger
	}

	defer func() { _ = resp.Body.Close() }()

	var status sealStatusResponse

	err = json.NewDecoder(io.LimitReader(resp.Body, vaultProbeBodyLimit)).Decode(&status)
	if err != nil || status.Initialized == nil || status.Sealed == nil {
		return stranger
	}

	return vaultProbe{
		state:       vaultProbeVault,
		initialized: *status.Initialized,
		sealed:      *status.Sealed,
		storageType: status.StorageType,
	}
}

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
