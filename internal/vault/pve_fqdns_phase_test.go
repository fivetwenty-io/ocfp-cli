package vault

import (
	"bytes"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeLogSafe records every payload sent to SetMultiple on top of the
// in-memory fakeSafe, so a test can assert what was sent as well as what
// the record ended up holding.
type writeLogSafe struct {
	*fakeSafe

	sent map[string]map[string]interface{}
}

func newWriteLogSafe() *writeLogSafe {
	return &writeLogSafe{fakeSafe: newFakeSafe(), sent: map[string]map[string]interface{}{}}
}

func (w *writeLogSafe) SetMultiple(path string, data map[string]interface{}) error {
	if w.sent[path] == nil {
		w.sent[path] = map[string]interface{}{}
	}

	for k, v := range data {
		w.sent[path][k] = v
	}

	return w.fakeSafe.SetMultiple(path, data)
}

func (w *writeLogSafe) Set(path, key string, value interface{}) error {
	return w.SetMultiple(path, map[string]interface{}{key: value})
}

func fqdnPhaseConfig() *config.Config {
	return &config.Config{ //nolint:exhaustruct // only FQDN derivation inputs matter
		Provider: "pve",
		Network:  config.NetworkConfig{CIDR: "10.64.64.0/19"}, //nolint:exhaustruct
		FQDNs:    &config.FQDNConfig{Base: "ocf.example.io"},
	}
}

func newFQDNPhaseProvider(cfg *config.Config, safe SafeInterface) *PVEVaultProvider {
	provider := newTestPVEProviderWithFakeSafe(cfg, newFakeSafe())
	provider.Safe = safe

	return provider
}

func TestFQDNsPhase_WritesOnlyMissingKeys(t *testing.T) {
	safe := newWriteLogSafe()
	provider := newFQDNPhaseProvider(fqdnPhaseConfig(), safe)
	path := provider.PathBuilder.GetFQDNsPath(MgmtEnvType)

	require.NoError(t, safe.fakeSafe.SetMultiple(path, map[string]interface{}{
		"shield": "kept.example.io",
	}))

	require.NoError(t, provider.ConfigureMissingFQDNs(false, nil))

	assert.NotContains(t, safe.sent[path], "shield", "an existing key must not be sent")
	assert.Equal(t, "prometheus."+"ocf.example.io", safe.sent[path]["prometheus"])
	assert.Equal(t, MgmtEnvType, safe.sent[path]["env_type"])
	assert.Equal(t, "ocf.example.io", safe.sent[path]["base"])
}

func TestFQDNsPhase_ExistingKeysUntouchedWithoutForce(t *testing.T) {
	safe := newWriteLogSafe()
	provider := newFQDNPhaseProvider(fqdnPhaseConfig(), safe)

	for _, env := range []string{MgmtEnvType, OCFEnvType} {
		require.NoError(t, safe.fakeSafe.SetMultiple(provider.PathBuilder.GetFQDNsPath(env), map[string]interface{}{
			"env_type": "custom",
			"base":     "old.example.io",
			"shield":   "old-shield.example.io",
		}))
	}

	require.NoError(t, provider.ConfigureMissingFQDNs(false, nil))

	for _, env := range []string{MgmtEnvType, OCFEnvType} {
		rec := safe.data[provider.PathBuilder.GetFQDNsPath(env)]
		assert.Equal(t, "custom", rec["env_type"])
		assert.Equal(t, "old.example.io", rec["base"])
		assert.Equal(t, "old-shield.example.io", rec["shield"])
	}
}

func TestFQDNsPhase_ForceOverwrites(t *testing.T) {
	safe := newWriteLogSafe()
	provider := newFQDNPhaseProvider(fqdnPhaseConfig(), safe)
	path := provider.PathBuilder.GetFQDNsPath(MgmtEnvType)

	require.NoError(t, safe.fakeSafe.SetMultiple(path, map[string]interface{}{
		"shield": "old-shield.example.io",
		"base":   "old.example.io",
	}))

	require.NoError(t, provider.ConfigureMissingFQDNs(true, nil))

	assert.Equal(t, "shield.ocf.example.io", safe.data[path]["shield"])
	assert.Equal(t, "ocf.example.io", safe.data[path]["base"])
}

func TestFQDNsPhase_ExplicitConfigBeatsDerivedDefault(t *testing.T) {
	cfg := fqdnPhaseConfig()
	cfg.FQDNs.Mgmt = map[string]string{"shield": "backups.custom.example.io"}

	safe := newWriteLogSafe()
	provider := newFQDNPhaseProvider(cfg, safe)

	require.NoError(t, provider.ConfigureMissingFQDNs(false, nil))

	path := provider.PathBuilder.GetFQDNsPath(MgmtEnvType)
	assert.Equal(t, "backups.custom.example.io", safe.data[path]["shield"])

	// The phase and the full-populate writer must agree.
	full := newWriteLogSafe()
	fullProvider := newFQDNPhaseProvider(cfg, full)
	require.NoError(t, fullProvider.ConfigureFQDNs("", MgmtEnvType, nil, 1, 1))
	assert.Equal(t, full.data[path], safe.data[path])
}

func TestFQDNsPhase_UnsentKeySurvivesWrite(t *testing.T) {
	// The merge itself is covered against the real Safe in
	// TestSafeSetMultiple_MergesIntoExistingRecord.
	safe := newWriteLogSafe()
	provider := newFQDNPhaseProvider(fqdnPhaseConfig(), safe)
	mgmtPath := provider.PathBuilder.GetFQDNsPath(MgmtEnvType)
	require.NoError(t, safe.fakeSafe.SetMultiple(mgmtPath, map[string]interface{}{"extra": "extra.example.io"}))
	require.NoError(t, provider.ConfigureMissingFQDNs(true, nil))
	assert.Equal(t, "extra.example.io", safe.data[mgmtPath]["extra"], "keys the phase does not derive survive even under force")
}

func TestFQDNsPhase_DryRunListsAdditionsAndWritesNothing(t *testing.T) {
	safe := newWriteLogSafe()
	mgr := newReservedIPsScopeTestManager(fqdnPhaseConfig(), safe)
	mgmtPath := NewPathBuilder(fqdnPhaseConfig(), "test-bloc").GetFQDNsPath(MgmtEnvType)

	require.NoError(t, safe.fakeSafe.SetMultiple(mgmtPath, map[string]interface{}{"shield": "old.example.io"}))

	var out bytes.Buffer

	err := mgr.populateDryRun(&PopulateOptions{Subcommand: PhaseFQDNs, DryRun: true}, safe, "target", &out) //nolint:exhaustruct
	require.NoError(t, err)

	assert.Empty(t, safe.sent, "a dry run must write nothing")
	assert.Contains(t, out.String(), "[DRY RUN]")
	assert.Contains(t, out.String(), "mgmt")
	assert.Contains(t, out.String(), "ocf")
	assert.Contains(t, out.String(), "prometheus = prometheus.ocf.example.io")
	assert.NotContains(t, out.String(), "shield = old.example.io")
	assert.NotContains(t, out.String(), "overwrite")

	out.Reset()

	err = mgr.populateDryRun(&PopulateOptions{Subcommand: PhaseFQDNs, DryRun: true, Force: true}, safe, "target", &out) //nolint:exhaustruct
	require.NoError(t, err)

	assert.Empty(t, safe.sent)
	assert.Contains(t, out.String(), "overwrite")
	assert.Contains(t, out.String(), "shield: old.example.io -> shield.ocf.example.io")
}

func TestFQDNsPhase_ManagerDispatchWritesOnlyFQDNPaths(t *testing.T) {
	safe := newWriteLogSafe()
	mgr := newReservedIPsScopeTestManager(fqdnPhaseConfig(), safe)

	require.NoError(t, mgr.populate(&PopulateOptions{Subcommand: PhaseFQDNs})) //nolint:exhaustruct

	require.Len(t, safe.sent, 2)

	for path := range safe.sent {
		assert.Contains(t, path, "/fqdns")
	}
}
