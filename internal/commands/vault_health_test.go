package commands

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVaultRespondsOnAddr_LiveVault asserts a healthy inception vault is
// detected. This is the check that guards cleanupExistingVault: when it fails,
// ensureInceptionVault skips its idempotency fast-path and deletes the vault
// directory along with root.key and unseal.keys.
func TestVaultRespondsOnAddr_LiveVault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/sys/seal-status") {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"initialized":true,"sealed":false}`))
	}))
	defer srv.Close()

	assert.True(t, vaultRespondsOnAddr(context.Background(), srv.URL),
		"an initialized, unsealed vault must be reported as running")
}

// TestVaultRespondsOnAddr_SealedVault asserts a sealed vault still counts as
// running. It holds the bloc's data; deleting it would be the same data loss
// as deleting an unsealed one.
func TestVaultRespondsOnAddr_SealedVault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"initialized":true,"sealed":true}`))
	}))
	defer srv.Close()

	assert.True(t, vaultRespondsOnAddr(context.Background(), srv.URL),
		"a sealed vault is still running and still holds secrets")
}

// TestVaultRespondsOnAddr_NothingListening asserts a genuinely absent vault is
// reported as not running, so the check still discriminates.
func TestVaultRespondsOnAddr_NothingListening(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	addr := srv.URL
	srv.Close()

	assert.False(t, vaultRespondsOnAddr(context.Background(), addr),
		"a closed port must be reported as not running")
}

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

// A closed port can be handed to another listener the moment it is freed,
// since tests across packages run at the same time. A port that turns out to
// be taken again is retried with a new one; a port that still refuses
// connections must have been reported as stopped.
func TestProbeInceptionVault_ClosedPortIsStopped(t *testing.T) {
	t.Parallel()

	for range 5 {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		addr := srv.URL
		srv.Close()

		state := probeInceptionVault(context.Background(), addr).state
		if state == vaultProbeStopped {
			return
		}

		conn, err := net.DialTimeout("tcp", strings.TrimPrefix(addr, "http://"), time.Second)
		require.NoError(t, err, "a port that refuses connections was reported as %v", state)

		_ = conn.Close()
	}

	t.Fatal("every closed port was taken again before it could be probed")
}
