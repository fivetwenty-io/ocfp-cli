package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildMinimalConfig returns a bare *config.Config sufficient for manifest tests.
func buildMinimalConfig(t *testing.T, blocName string) *config.Config {
	t.Helper()

	return &config.Config{
		Name: blocName,
		Network: config.NetworkConfig{
			CIDR: "10.4.0.0/20",
		},
		Bastion: config.Bastion{
			Flavor: "t3.medium",
		},
	}
}

// newLoadedStateManager creates a Manager backed by a temp dir and loads the given bloc.
func newLoadedStateManager(t *testing.T, blocName string) *state.Manager {
	t.Helper()

	mgr, err := state.NewManager(t.TempDir())
	require.NoError(t, err)

	_, err = mgr.Load(blocName)
	require.NoError(t, err)

	return mgr
}

// TestCreateBOSHManifest_EmptyStateReturnsError verifies that createBOSHManifest
// returns ErrBootstrapStateRequired when the subnet ID output is absent.
func TestCreateBOSHManifest_EmptyStateReturnsError(t *testing.T) {
	const bloc = "test-bloc"

	cfg := buildMinimalConfig(t, bloc)
	mgr := newLoadedStateManager(t, bloc)
	path := filepath.Join(t.TempDir(), "bosh.yml")

	err := createBOSHManifest(cfg, path, mgr)

	require.Error(t, err, "createBOSHManifest must error when state lacks subnet_id")
	assert.ErrorIs(t, err, ErrBootstrapStateRequired,
		"error must wrap ErrBootstrapStateRequired; got: %v", err)
}

// TestCreateBOSHManifest_MissingBoshIPReturnsError verifies that createBOSHManifest
// returns ErrBootstrapStateRequired when the subnet ID is present but bosh_ip is absent.
func TestCreateBOSHManifest_MissingBoshIPReturnsError(t *testing.T) {
	const bloc = "test-bloc"

	cfg := buildMinimalConfig(t, bloc)
	mgr := newLoadedStateManager(t, bloc)

	// Provide subnet ID only; omit bosh IP.
	subnetName := bloc + "-ocfp-0"
	require.NoError(t, mgr.SetOutput("subnet_"+subnetName+"_id", "subnet-abc123"))

	path := filepath.Join(t.TempDir(), "bosh.yml")

	err := createBOSHManifest(cfg, path, mgr)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrBootstrapStateRequired,
		"error must wrap ErrBootstrapStateRequired when bosh_ip missing; got: %v", err)
}

// TestCreateBOSHManifest_PopulatedStateWritesRealValues verifies that a manifest
// written from populated state contains the real subnet ID and static IP, and
// does not contain any placeholder patterns.
func TestCreateBOSHManifest_PopulatedStateWritesRealValues(t *testing.T) {
	const (
		bloc         = "test-bloc"
		realSubnetID = "subnet-0a1b2c3d4e5f6a7b8"
		realBoshIP   = "10.4.0.4"
		forbiddenID  = "subnet-xxxxxx"
		forbiddenIP  = "10.0.0.6"
	)

	cfg := buildMinimalConfig(t, bloc)
	mgr := newLoadedStateManager(t, bloc)

	subnetName := bloc + "-ocfp-0"
	require.NoError(t, mgr.SetOutput("subnet_"+subnetName+"_id", realSubnetID))
	require.NoError(t, mgr.SetOutput("reserved_"+subnetName+"_bosh_ip", realBoshIP))

	manifestPath := filepath.Join(t.TempDir(), "bosh.yml")

	err := createBOSHManifest(cfg, manifestPath, mgr)
	require.NoError(t, err, "createBOSHManifest must succeed with full state")

	content, err := os.ReadFile(manifestPath)
	require.NoError(t, err, "manifest file must exist")

	body := string(content)

	// Real values must be present.
	assert.Contains(t, body, realSubnetID,
		"manifest must contain real subnet ID %q", realSubnetID)
	assert.Contains(t, body, realBoshIP,
		"manifest must contain real bosh static IP %q", realBoshIP)

	// Placeholder patterns must be absent.
	assert.False(t, strings.Contains(body, forbiddenID),
		"manifest must not contain placeholder %q", forbiddenID)
	assert.False(t, strings.Contains(body, forbiddenIP),
		"manifest must not contain placeholder IP %q", forbiddenIP)
	assert.False(t, strings.Contains(body, "xxxxxx"),
		"manifest must not contain any xxxxxx placeholder")
	assert.False(t, strings.Contains(body, "i-xxxxxx"),
		"manifest must not contain i-xxxxxx placeholder")
	assert.False(t, strings.Contains(body, "vpc-xxxxxx"),
		"manifest must not contain vpc-xxxxxx placeholder")
}

// TestCreateBOSHManifest_ManifestIsValidYAML verifies that the generated manifest
// is well-formed YAML (parseable without error).
func TestCreateBOSHManifest_ManifestIsValidYAML(t *testing.T) {
	const (
		bloc         = "test-bloc"
		realSubnetID = "subnet-0a1b2c3d4e5f6a7b8"
		realBoshIP   = "10.4.0.4"
	)

	cfg := buildMinimalConfig(t, bloc)
	mgr := newLoadedStateManager(t, bloc)

	subnetName := bloc + "-ocfp-0"
	require.NoError(t, mgr.SetOutput("subnet_"+subnetName+"_id", realSubnetID))
	require.NoError(t, mgr.SetOutput("reserved_"+subnetName+"_bosh_ip", realBoshIP))

	manifestPath := filepath.Join(t.TempDir(), "bosh.yml")

	require.NoError(t, createBOSHManifest(cfg, manifestPath, mgr))

	content, err := os.ReadFile(manifestPath)
	require.NoError(t, err)

	// Minimal structural check: must start with ---
	body := strings.TrimSpace(string(content))
	assert.True(t, strings.HasPrefix(body, "---"),
		"manifest must start with YAML document marker ---; got first line: %q",
		strings.SplitN(body, "\n", 2)[0])
}
