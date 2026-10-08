package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"syscall"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/keyfile"
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
	return probeInceptionVaultWith(ctx, http.DefaultClient, addr)
}

// probeInceptionVaultWith probes addr with the given HTTP client, so a test
// can supply the dial rather than race other tests for a closed port.
func probeInceptionVaultWith(ctx context.Context, client *http.Client, addr string) vaultProbe {
	ctx, cancel := context.WithTimeout(ctx, vaultHealthCheckTimeout)
	defer cancel()

	stranger := vaultProbe{state: vaultProbeStranger}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr+"/v1/sys/seal-status", nil)
	if err != nil {
		return stranger
	}

	resp, err := client.Do(req)
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

// ErrInceptionRootTokenRefused reports a running inception vault that does
// not take the token saved in root.key. A restart opens the vault with that
// token, so a vault that refuses it could not be reopened after a stop.
var ErrInceptionRootTokenRefused = errors.New("the running inception vault refuses the token in root.key")

// checkInceptionRootToken asks the bloc's running vault whether it takes the
// token in root.key.
func checkInceptionRootToken(ctx context.Context, paths map[string]string) error {
	return checkInceptionRootTokenWith(ctx, http.DefaultClient, "http://127.0.0.1:"+paths["port"],
		paths["rootKeyFile"])
}

// checkInceptionRootTokenWith asks the vault at addr to look up the token in
// rootKeyFile. The token travels only in the request header, and no error
// quotes it. A vault that answers 401 or 403 refuses it, and any other answer,
// or none, leaves the question open, which is an error of its own, because a
// sealed or struggling vault cannot vouch for a token either.
func checkInceptionRootTokenWith(ctx context.Context, client *http.Client, addr, rootKeyFile string) error {
	token, err := keyfile.Read(rootKeyFile)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, vaultHealthCheckTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr+"/v1/auth/token/lookup-self", nil)
	if err != nil {
		return fmt.Errorf("cannot ask the vault at %s whether it takes the token in %s: %w", addr, rootKeyFile, err)
	}

	req.Header.Set("X-Vault-Token", token)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("cannot ask the vault at %s whether it takes the token in %s: %w", addr, rootKeyFile, err)
	}

	defer func() { _ = resp.Body.Close() }()

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, vaultProbeBodyLimit))

	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: %s", ErrInceptionRootTokenRefused, rootKeyFile)
	default:
		return fmt.Errorf("cannot tell whether the vault at %s takes the token in %s, because it answered %s",
			addr, rootKeyFile, resp.Status)
	}
}
