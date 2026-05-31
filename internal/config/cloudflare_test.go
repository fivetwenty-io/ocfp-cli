package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloudflareConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     *CloudflareConfig
		wantErr bool
	}{
		{"nil is valid", nil, false},
		{"empty is valid", &CloudflareConfig{}, false},
		{"only literal token valid", &CloudflareConfig{APIToken: "cf-tok"}, false},
		{"only vault path valid", &CloudflareConfig{APITokenVaultPath: "secret/config/x/cloudflare:api_token"}, false},
		{"both set invalid", &CloudflareConfig{APIToken: "cf-tok", APITokenVaultPath: "secret/x:api_token"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if tt.wantErr {
				assert.ErrorIs(t, err, ErrCloudflareAPITokenConflict)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCloudflareEnabled(t *testing.T) {
	t.Parallel()

	assert.False(t, CloudflareEnabled(nil))
	assert.False(t, CloudflareEnabled(&CloudflareConfig{}))
	assert.False(t, CloudflareEnabled(&CloudflareConfig{Enabled: boolPtr(false)}))
	assert.True(t, CloudflareEnabled(&CloudflareConfig{Enabled: boolPtr(true)}))
}

func TestConfigEmbedsCloudflare(t *testing.T) {
	t.Parallel()
	cf := &ConfigFile{Cloudflare: &CloudflareConfig{Zone: "fivetwenty.io"}}
	bloc := &Config{Cloudflare: &CloudflareConfig{Origin: "https://10.64.64.38"}}
	require.NotNil(t, cf.Cloudflare)
	require.NotNil(t, bloc.Cloudflare)
	assert.Equal(t, "fivetwenty.io", cf.Cloudflare.Zone)
	assert.Equal(t, "https://10.64.64.38", bloc.Cloudflare.Origin)
}

func TestMergeCloudflareDefaults(t *testing.T) {
	t.Parallel()

	t.Run("nil bloc adopts defaults as independent clone", func(t *testing.T) {
		t.Parallel()
		bloc := &Config{}
		defaults := &CloudflareConfig{
			Enabled:   boolPtr(true),
			Zone:      "fivetwenty.io",
			Origin:    "https://10.64.64.20",
			SSHOrigin: "ssh://10.64.64.37:2222",
		}
		mergeCloudflareDefaults(bloc, defaults)
		require.NotNil(t, bloc.Cloudflare)
		assert.Equal(t, "fivetwenty.io", bloc.Cloudflare.Zone)
		require.NotNil(t, bloc.Cloudflare.Enabled)
		assert.True(t, *bloc.Cloudflare.Enabled)
		*defaults.Enabled = false
		assert.True(t, *bloc.Cloudflare.Enabled, "clone must be independent of source")
	})

	t.Run("per-bloc overrides win, defaults fill gaps", func(t *testing.T) {
		t.Parallel()
		bloc := &Config{Cloudflare: &CloudflareConfig{Origin: "https://10.64.64.38"}}
		defaults := &CloudflareConfig{Origin: "https://10.64.64.20", Zone: "fivetwenty.io"}
		mergeCloudflareDefaults(bloc, defaults)
		assert.Equal(t, "https://10.64.64.38", bloc.Cloudflare.Origin, "bloc origin wins")
		assert.Equal(t, "fivetwenty.io", bloc.Cloudflare.Zone, "default fills gap")
	})

	t.Run("auth-token inheritance is paired", func(t *testing.T) {
		t.Parallel()
		bloc := &Config{Cloudflare: &CloudflareConfig{APIToken: "bloc-literal"}}
		defaults := &CloudflareConfig{APITokenVaultPath: "secret/x:api_token"}
		mergeCloudflareDefaults(bloc, defaults)
		assert.Equal(t, "bloc-literal", bloc.Cloudflare.APIToken)
		assert.Empty(t, bloc.Cloudflare.APITokenVaultPath, "must not inherit vault path when bloc set literal")
	})
}
