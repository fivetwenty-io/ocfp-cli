package commands

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
