package commands

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sealStatusServer answers /v1/sys/seal-status with body and status.
func sealStatusServer(t *testing.T, status int, contentType, body string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sys/seal-status" {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return srv
}

func TestProbeInceptionVault_RaftVault(t *testing.T) {
	t.Parallel()

	srv := sealStatusServer(t, http.StatusOK, "application/json",
		`{"type":"shamir","initialized":true,"sealed":false,"storage_type":"raft","version":"2.7.1"}`)

	probe := probeInceptionVault(context.Background(), srv.URL)

	assert.Equal(t, vaultProbeVault, probe.state)
	assert.True(t, probe.initialized)
	assert.False(t, probe.sealed)
	assert.Equal(t, "raft", probe.storageType)
}

func TestProbeInceptionVault_SealedFileVault(t *testing.T) {
	t.Parallel()

	srv := sealStatusServer(t, http.StatusOK, "application/json",
		`{"type":"shamir","initialized":true,"sealed":true,"storage_type":"file"}`)

	probe := probeInceptionVault(context.Background(), srv.URL)

	assert.Equal(t, vaultProbeVault, probe.state)
	assert.True(t, probe.initialized)
	assert.True(t, probe.sealed)
	assert.Equal(t, "file", probe.storageType)
}

// Older engines omit storage_type. The probe must still recognise the vault
// and leave the storage unnamed, so the caller falls back to the on-disk
// classification.
func TestProbeInceptionVault_VaultWithoutStorageType(t *testing.T) {
	t.Parallel()

	srv := sealStatusServer(t, http.StatusOK, "application/json", `{"initialized":false,"sealed":true}`)

	probe := probeInceptionVault(context.Background(), srv.URL)

	assert.Equal(t, vaultProbeVault, probe.state)
	assert.False(t, probe.initialized)
	assert.True(t, probe.sealed)
	assert.Empty(t, probe.storageType)
}

// Something that is not a vault holds the port. Treating it as stopped would
// start a vault that cannot bind; treating it as a vault would skip the start.
func TestProbeInceptionVault_StrangerOnThePort(t *testing.T) {
	t.Parallel()

	srv := sealStatusServer(t, http.StatusNotFound, "text/html", "<html><body>404 Not Found</body></html>")

	assert.Equal(t, vaultProbeStranger, probeInceptionVault(context.Background(), srv.URL).state)

	other := sealStatusServer(t, http.StatusOK, "application/json", `{"status":"ok"}`)

	assert.Equal(t, vaultProbeStranger, probeInceptionVault(context.Background(), other.URL).state,
		"JSON without the seal-status fields is not a vault")
}

// A listener that never answers is not proof the vault is stopped. It is
// reported as a stranger so the caller changes nothing.
func TestProbeInceptionVault_NoAnswerInTimeIsAStranger(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	assert.Equal(t, vaultProbeStranger, probeInceptionVault(ctx, srv.URL).state)
}

// The probe must report a refused connection as a stopped vault. The dial is
// injected to refuse, because a real closed port can be taken by another test
// between the close and the probe.
func TestProbeInceptionVault_ClosedPortIsStopped(t *testing.T) {
	t.Parallel()

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
		},
	}}

	state := probeInceptionVaultWith(context.Background(), client, "http://127.0.0.1:1").state
	assert.Equal(t, vaultProbeStopped, state)
}

// tokenLookupServer answers /v1/auth/token/lookup-self with status and
// records the token each request carried.
func tokenLookupServer(t *testing.T, status int, seen *[]string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/auth/token/lookup-self" {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		*seen = append(*seen, r.Header.Get("X-Vault-Token"))

		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)

	return srv
}

// The root token is checked by asking the running vault to look it up, with
// the token from root.key in the request header and nowhere else.
func TestCheckInceptionRootToken(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		status  int
		refused bool
		ok      bool
	}{
		"taken":         {status: http.StatusOK, ok: true},
		"forbidden":     {status: http.StatusForbidden, refused: true},
		"unauthorized":  {status: http.StatusUnauthorized, refused: true},
		"sealed":        {status: http.StatusServiceUnavailable},
		"server failed": {status: http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rootKey := filepath.Join(t.TempDir(), "root.key")
			require.NoError(t, os.WriteFile(rootKey, []byte("s.ROOT-TOKEN-SENTINEL\n"), 0o600))

			var seen []string

			srv := tokenLookupServer(t, tc.status, &seen)

			err := checkInceptionRootTokenWith(context.Background(), srv.Client(), srv.URL, rootKey)
			assert.Equal(t, []string{"s.ROOT-TOKEN-SENTINEL"}, seen, "root.key's token, trimmed, is sent once")

			if tc.ok {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
			assert.Equal(t, tc.refused, errors.Is(err, ErrInceptionRootTokenRefused))
			assert.Contains(t, err.Error(), rootKey)
			assert.NotContains(t, err.Error(), "SENTINEL")
		})
	}
}

// A vault that does not answer cannot vouch for the token, which is not the
// same as refusing it.
func TestCheckInceptionRootToken_NoAnswer(t *testing.T) {
	t.Parallel()

	rootKey := filepath.Join(t.TempDir(), "root.key")
	require.NoError(t, os.WriteFile(rootKey, []byte("s.ROOT-TOKEN-SENTINEL\n"), 0o600))

	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.URL
	srv.Close()

	err := checkInceptionRootTokenWith(context.Background(), http.DefaultClient, addr, rootKey)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrInceptionRootTokenRefused)
	assert.NotContains(t, err.Error(), "SENTINEL")
}
