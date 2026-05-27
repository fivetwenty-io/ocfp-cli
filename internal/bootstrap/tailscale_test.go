package bootstrap

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
)

func boolPtr(b bool) *bool { return &b }

// fakeTailscaleSafe is a minimal SafeInterface stub backed by an in-memory map.
// Only GetString is exercised by the resolver; all other methods are no-ops.
type fakeTailscaleSafe struct {
	values map[string]map[string]string
	err    error
}

func newFakeTailscaleSafe() *fakeTailscaleSafe {
	return &fakeTailscaleSafe{values: map[string]map[string]string{}}
}

func (f *fakeTailscaleSafe) set(path, key, val string) {
	if _, ok := f.values[path]; !ok {
		f.values[path] = map[string]string{}
	}

	f.values[path][key] = val
}

func (f *fakeTailscaleSafe) GetString(path, key string) (string, error) {
	if f.err != nil {
		return "", f.err
	}

	if m, ok := f.values[path]; ok {
		if v, ok := m[key]; ok {
			return v, nil
		}
	}

	return "", errors.New("not found")
}

func (f *fakeTailscaleSafe) Set(_, _ string, _ interface{}) error                 { return nil }
func (f *fakeTailscaleSafe) SetMultiple(_ string, _ map[string]interface{}) error { return nil }
func (f *fakeTailscaleSafe) Get(_, _ string) (interface{}, error)                 { return nil, nil }
func (f *fakeTailscaleSafe) GetAll(_ string) (map[string]interface{}, error) {
	return nil, nil
}
func (f *fakeTailscaleSafe) Exists(_ string) (bool, error)   { return false, nil }
func (f *fakeTailscaleSafe) Delete(_, _ string) error        { return nil }
func (f *fakeTailscaleSafe) List(_ string) ([]string, error) { return nil, nil }
func (f *fakeTailscaleSafe) Export(_ string) (map[string]interface{}, error) {
	return nil, nil
}
func (f *fakeTailscaleSafe) Import(_ string, _ map[string]interface{}) error { return nil }
func (f *fakeTailscaleSafe) GetEngineInfo(_ string) (*vault.EngineInfo, error) {
	return nil, nil
}
func (f *fakeTailscaleSafe) MustGet(_, _ string) interface{}     { return nil }
func (f *fakeTailscaleSafe) GetJSON(_, _ string) ([]byte, error) { return nil, nil }

func newTestManager(t *testing.T, cfg *config.Config, safe vault.SafeInterface) *Manager {
	t.Helper()

	stateDir := t.TempDir()

	sm, err := state.NewManager(stateDir)
	require.NoError(t, err)

	m := NewManager(cfg, nil, sm, &Options{BlocName: "test-bloc"})
	if safe != nil {
		m.SetSafe(safe)
	}

	return m
}

// TestResolveBastionTailscaleAuthKey_NoConfigNoFallback proves the legacy
// hard-coded vault path is no longer consulted when the merged tailscale
// config has neither auth_key nor auth_key_vault_path set.
func TestResolveBastionTailscaleAuthKey_NoConfigNoFallback(t *testing.T) {
	t.Parallel()

	safe := newFakeTailscaleSafe()
	safe.set("secret/ocfp/tailscale/auth_key", "value", "legacy-key-must-not-be-read")

	cfg := &config.Config{} // no Tailscale config at all

	m := newTestManager(t, cfg, safe)

	key := m.resolveBastionTailscaleAuthKey()

	assert.Equal(t, "", key, "must not fall back to legacy vault path")
}

// TestResolveBastionTailscaleAuthKey_Literal returns the configured literal
// without touching vault.
func TestResolveBastionTailscaleAuthKey_Literal(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Tailscale: &config.TailscaleConfig{AuthKey: "tskey-literal"}}

	m := newTestManager(t, cfg, nil)

	key := m.resolveBastionTailscaleAuthKey()

	assert.Equal(t, "tskey-literal", key)
}

// TestResolveBastionTailscaleAuthKey_VaultPath parses the "path:key" form and
// reads from the injected vault safe.
func TestResolveBastionTailscaleAuthKey_VaultPath(t *testing.T) {
	t.Parallel()

	safe := newFakeTailscaleSafe()
	safe.set("secret/team/ts", "authkey", "tskey-from-vault")

	cfg := &config.Config{Tailscale: &config.TailscaleConfig{
		AuthKeyVaultPath: "secret/team/ts:authkey",
	}}

	m := newTestManager(t, cfg, safe)

	key := m.resolveBastionTailscaleAuthKey()

	assert.Equal(t, "tskey-from-vault", key)
}

// TestResolveBastionTailscaleAuthKey_VaultMissingSoftSkip returns "" (soft
// skip) when the configured vault path is unreadable; bootstrap must not
// abort the bastion just because tailscale isn't available.
func TestResolveBastionTailscaleAuthKey_VaultMissingSoftSkip(t *testing.T) {
	t.Parallel()

	safe := newFakeTailscaleSafe() // empty store

	cfg := &config.Config{Tailscale: &config.TailscaleConfig{
		AuthKeyVaultPath: "secret/missing:key",
	}}

	m := newTestManager(t, cfg, safe)

	key := m.resolveBastionTailscaleAuthKey()

	assert.Equal(t, "", key)
}

// TestBastionTailscaleSpec_AppliesConfigOverrides verifies the spec inherits
// config fields (tags, accept_dns, ssh, exit_node, hostname) when set, falling
// back to OCFP defaults otherwise.
func TestBastionTailscaleSpec_AppliesConfigOverrides(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Tailscale: &config.TailscaleConfig{
		Enabled:      boolPtr(true),
		AuthKey:      "tskey-1",
		Tags:         []string{"tag:custom"},
		AcceptDNS:    boolPtr(true),
		AcceptRoutes: boolPtr(true),
		SSH:          boolPtr(false),
		ExitNode:     "exit.example",
		Hostname:     "custom-host",
	}}

	m := newTestManager(t, cfg, nil)

	spec := m.bastionTailscaleSpec("derived-host", "10.64.64.3", 18)

	require.NotNil(t, spec)
	assert.Equal(t, "tskey-1", spec.AuthKey)
	assert.Equal(t, "custom-host", spec.Hostname, "config hostname overrides derived")
	assert.Equal(t, []string{"tag:custom"}, spec.Tags)
	assert.True(t, spec.AcceptDNS)
	assert.True(t, spec.AcceptRoutes)
	assert.False(t, spec.SSH)
	assert.Equal(t, "exit.example", spec.ExitNode)
	assert.Equal(t, "10.64.64.0/18", spec.AdvertiseRoutes, "derived when config field empty")
}

// TestBastionTailscaleSpec_NilWhenNoAuthKey returns nil so the PVE provider
// skips SMBIOS injection entirely.
func TestBastionTailscaleSpec_NilWhenNoAuthKey(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	m := newTestManager(t, cfg, nil)

	spec := m.bastionTailscaleSpec("derived-host", "10.64.64.3", 18)

	assert.Nil(t, spec)
}

// T49: TestBastionTailscaleSpec_DisabledReturnsNil asserts that
// bastionTailscaleSpec returns nil when Enabled is false, even when a valid
// auth key is configured. An explicit opt-in (enabled: true) is required.
func TestBastionTailscaleSpec_DisabledReturnsNil(t *testing.T) {
	t.Parallel()

	t.Run("enabled nil returns nil (default-false)", func(t *testing.T) {
		t.Parallel()

		// Auth key is present but Enabled is nil — must still return nil.
		cfg := &config.Config{Tailscale: &config.TailscaleConfig{AuthKey: "tskey-1"}}
		m := newTestManager(t, cfg, nil)

		spec := m.bastionTailscaleSpec("bastion-host", "10.64.64.3", 18)

		assert.Nil(t, spec, "Enabled nil must disable Tailscale (default-false)")
	})

	t.Run("enabled explicit false returns nil", func(t *testing.T) {
		t.Parallel()

		cfg := &config.Config{Tailscale: &config.TailscaleConfig{
			Enabled: boolPtr(false),
			AuthKey: "tskey-1",
		}}
		m := newTestManager(t, cfg, nil)

		spec := m.bastionTailscaleSpec("bastion-host", "10.64.64.3", 18)

		assert.Nil(t, spec, "Enabled false must disable Tailscale even with valid auth key")
	})

	t.Run("nil config returns nil", func(t *testing.T) {
		t.Parallel()

		m := newTestManager(t, nil, nil)

		spec := m.bastionTailscaleSpec("bastion-host", "10.64.64.3", 18)

		assert.Nil(t, spec, "nil config must not panic and must return nil")
	})
}

// TestBastionTailscaleSpec_DefaultsWhenConfigSparse exercises the bootstrap
// fallbacks when only auth_key is set; tags default to tag:ocfp-bastion, SSH
// defaults to true, accept_dns and accept_routes default to false.
func TestBastionTailscaleSpec_DefaultsWhenConfigSparse(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Tailscale: &config.TailscaleConfig{Enabled: boolPtr(true), AuthKey: "tskey-1"}}
	m := newTestManager(t, cfg, nil)

	spec := m.bastionTailscaleSpec("bastion-host", "10.64.64.3", 18)

	require.NotNil(t, spec)
	assert.Equal(t, "bastion-host", spec.Hostname, "fallback to derived host")
	assert.Equal(t, []string{"tag:ocfp-bastion"}, spec.Tags)
	assert.False(t, spec.AcceptDNS)
	assert.False(t, spec.AcceptRoutes)
	assert.True(t, spec.SSH)
	assert.Equal(t, "10.64.64.0/18", spec.AdvertiseRoutes)
}
