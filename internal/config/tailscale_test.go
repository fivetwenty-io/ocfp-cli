package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func boolPtr(b bool) *bool { return &b }

// TestTailscaleConfig_Validate covers mutual exclusion of AuthKey and
// AuthKeyVaultPath. A scope may set zero or one of them; setting both is
// an operator mistake the loader must reject.
func TestTailscaleConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     *TailscaleConfig
		wantErr bool
	}{
		{"nil tailscale is valid", nil, false},
		{"empty tailscale is valid", &TailscaleConfig{}, false},
		{"only literal is valid", &TailscaleConfig{AuthKey: "tskey-1"}, false},
		{"only vault path is valid", &TailscaleConfig{AuthKeyVaultPath: "secret/ts:key"}, false},
		{"both set is invalid", &TailscaleConfig{AuthKey: "tskey-1", AuthKeyVaultPath: "secret/ts:key"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestMergeTailscaleDefaults covers the precedence cases for the
// global -> per-bloc tailscale inheritance.
func TestMergeTailscaleDefaults(t *testing.T) {
	t.Parallel()

	t.Run("nil defaults leaves bloc unchanged", func(t *testing.T) {
		t.Parallel()

		bloc := &Config{Tailscale: &TailscaleConfig{AuthKey: "bloc-key"}}
		mergeTailscaleDefaults(bloc, nil)

		require.NotNil(t, bloc.Tailscale)
		assert.Equal(t, "bloc-key", bloc.Tailscale.AuthKey)
	})

	t.Run("nil bloc tailscale adopts defaults", func(t *testing.T) {
		t.Parallel()

		bloc := &Config{}
		defaults := &TailscaleConfig{
			AuthKey:         "global-key",
			Hostname:        "global-host",
			Tags:            []string{"tag:ocfp-bastion"},
			AcceptDNS:       boolPtr(false),
			AcceptRoutes:    boolPtr(false),
			SSH:             boolPtr(true),
			ExitNode:        "exit-1",
			AdvertiseRoutes: "10.0.0.0/24",
		}

		mergeTailscaleDefaults(bloc, defaults)

		require.NotNil(t, bloc.Tailscale)
		assert.Equal(t, "global-key", bloc.Tailscale.AuthKey)
		assert.Equal(t, "global-host", bloc.Tailscale.Hostname)
		assert.Equal(t, []string{"tag:ocfp-bastion"}, bloc.Tailscale.Tags)
		require.NotNil(t, bloc.Tailscale.AcceptDNS)
		assert.False(t, *bloc.Tailscale.AcceptDNS)
		require.NotNil(t, bloc.Tailscale.SSH)
		assert.True(t, *bloc.Tailscale.SSH)
		assert.Equal(t, "exit-1", bloc.Tailscale.ExitNode)
		assert.Equal(t, "10.0.0.0/24", bloc.Tailscale.AdvertiseRoutes)
	})

	t.Run("bloc overrides global field-by-field", func(t *testing.T) {
		t.Parallel()

		bloc := &Config{Tailscale: &TailscaleConfig{
			AuthKey:  "bloc-key",
			Hostname: "bloc-host",
			SSH:      boolPtr(false),
		}}
		defaults := &TailscaleConfig{
			AuthKey:      "global-key",
			Hostname:     "global-host",
			Tags:         []string{"tag:ocfp-bastion"},
			AcceptDNS:    boolPtr(true),
			AcceptRoutes: boolPtr(true),
			SSH:          boolPtr(true),
			ExitNode:     "exit-1",
		}

		mergeTailscaleDefaults(bloc, defaults)

		assert.Equal(t, "bloc-key", bloc.Tailscale.AuthKey, "bloc auth_key wins")
		assert.Equal(t, "bloc-host", bloc.Tailscale.Hostname, "bloc hostname wins")
		assert.Equal(t, []string{"tag:ocfp-bastion"}, bloc.Tailscale.Tags, "tags inherit when bloc unset")
		require.NotNil(t, bloc.Tailscale.AcceptDNS, "AcceptDNS inherited")
		assert.True(t, *bloc.Tailscale.AcceptDNS)
		require.NotNil(t, bloc.Tailscale.SSH, "bloc SSH explicit false wins")
		assert.False(t, *bloc.Tailscale.SSH, "explicit bloc false is not treated as unset")
		assert.Equal(t, "exit-1", bloc.Tailscale.ExitNode, "ExitNode inherited")
	})

	t.Run("bloc auth_key_vault_path blocks global auth_key inheritance", func(t *testing.T) {
		t.Parallel()

		bloc := &Config{Tailscale: &TailscaleConfig{AuthKeyVaultPath: "secret/ts:key"}}
		defaults := &TailscaleConfig{AuthKey: "global-literal"}

		mergeTailscaleDefaults(bloc, defaults)

		assert.Equal(t, "", bloc.Tailscale.AuthKey, "must not inherit global literal when bloc specifies vault path")
		assert.Equal(t, "secret/ts:key", bloc.Tailscale.AuthKeyVaultPath)
	})

	t.Run("nil bloc is safe", func(t *testing.T) {
		t.Parallel()

		assert.NotPanics(t, func() {
			mergeTailscaleDefaults(nil, &TailscaleConfig{AuthKey: "k"})
		})
	})
}

// T45: TailscaleEnabled returns false when cfg is nil.
func TestTailscaleEnabled_NilConfig(t *testing.T) {
	t.Parallel()

	assert.False(t, TailscaleEnabled(nil))
}

// T46: TailscaleEnabled returns false when cfg.Enabled pointer is nil
// (the zero/unset case — default-false semantics).
func TestTailscaleEnabled_NilPointer(t *testing.T) {
	t.Parallel()

	cfg := &TailscaleConfig{AuthKey: "tskey-1"} // Enabled omitted → nil

	assert.False(t, TailscaleEnabled(cfg))
}

// T47: TailscaleEnabled returns true only when *cfg.Enabled == true.
func TestTailscaleEnabled_True(t *testing.T) {
	t.Parallel()

	cfg := &TailscaleConfig{Enabled: boolPtr(true)}

	assert.True(t, TailscaleEnabled(cfg))
}

// T47b: TailscaleEnabled returns false when *cfg.Enabled == false,
// even when an auth key is configured.
func TestTailscaleEnabled_ExplicitFalse(t *testing.T) {
	t.Parallel()

	cfg := &TailscaleConfig{Enabled: boolPtr(false), AuthKey: "tskey-1"}

	assert.False(t, TailscaleEnabled(cfg))
}

// T48: mergeTailscaleDefaults propagates Enabled from global to bloc
// when bloc Enabled is nil, and bloc explicit false overrides global true.
func TestMergeTailscaleDefaults_BlocOverridesGlobal(t *testing.T) {
	t.Parallel()

	t.Run("global enabled propagates to bloc with nil Enabled", func(t *testing.T) {
		t.Parallel()

		bloc := &Config{Tailscale: &TailscaleConfig{AuthKey: "bloc-key"}}
		defaults := &TailscaleConfig{Enabled: boolPtr(true), AuthKey: "global-key"}

		mergeTailscaleDefaults(bloc, defaults)

		require.NotNil(t, bloc.Tailscale.Enabled, "Enabled must be propagated")
		assert.True(t, *bloc.Tailscale.Enabled)
		assert.Equal(t, "bloc-key", bloc.Tailscale.AuthKey, "bloc auth_key wins")
	})

	t.Run("bloc explicit false overrides global true", func(t *testing.T) {
		t.Parallel()

		bloc := &Config{Tailscale: &TailscaleConfig{Enabled: boolPtr(false), AuthKey: "bloc-key"}}
		defaults := &TailscaleConfig{Enabled: boolPtr(true), AuthKey: "global-key"}

		mergeTailscaleDefaults(bloc, defaults)

		require.NotNil(t, bloc.Tailscale.Enabled)
		assert.False(t, *bloc.Tailscale.Enabled, "bloc explicit false must not be overwritten by global true")
		assert.Equal(t, "bloc-key", bloc.Tailscale.AuthKey)
	})

	t.Run("nil bloc tailscale cloned from global with Enabled preserved", func(t *testing.T) {
		t.Parallel()

		bloc := &Config{}
		defaults := &TailscaleConfig{Enabled: boolPtr(true), AuthKey: "global-key"}

		mergeTailscaleDefaults(bloc, defaults)

		require.NotNil(t, bloc.Tailscale)
		require.NotNil(t, bloc.Tailscale.Enabled)
		assert.True(t, *bloc.Tailscale.Enabled)
		// Cloned pointer must be independent — mutating source must not affect clone.
		*defaults.Enabled = false
		assert.True(t, *bloc.Tailscale.Enabled, "clone must be independent of source")
	})
}
