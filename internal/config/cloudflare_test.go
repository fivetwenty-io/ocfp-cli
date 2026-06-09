package config

import (
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloudflareConfig_UnmarshalServices proves the services list parses through
// the same goccy/go-yaml path the loader uses, including the no_tls_verify *bool.
func TestCloudflareConfig_UnmarshalServices(t *testing.T) {
	t.Parallel()
	src := `enabled: true
zone: fivetwenty.io
services:
  - hostname: shield.system.ocf.wayne.lab.fivetwenty.io
    service: https://10.64.68.9
    no_tls_verify: true
  - hostname: grafana.system.ocf.wayne.lab.fivetwenty.io
    service: http://10.64.68.8:3000`
	var cf CloudflareConfig
	require.NoError(t, yaml.Unmarshal([]byte(src), &cf))
	require.Len(t, cf.Services, 2)
	assert.Equal(t, "shield.system.ocf.wayne.lab.fivetwenty.io", cf.Services[0].Hostname)
	assert.Equal(t, "https://10.64.68.9", cf.Services[0].Service)
	require.NotNil(t, cf.Services[0].NoTLSVerify)
	assert.True(t, *cf.Services[0].NoTLSVerify)
	assert.Equal(t, "http://10.64.68.8:3000", cf.Services[1].Service)
	assert.Nil(t, cf.Services[1].NoTLSVerify, "absent no_tls_verify stays nil")
	require.NoError(t, cf.Validate())
}

func TestCloudflareConfig_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     *CloudflareConfig
		wantErr error
	}{
		{"nil is valid", nil, nil},
		{"empty is valid", &CloudflareConfig{}, nil},
		{"only literal token valid", &CloudflareConfig{APIToken: "cf-tok"}, nil},
		{"only vault path valid", &CloudflareConfig{APITokenVaultPath: "secret/config/x/cloudflare:api_token"}, nil},
		{"both set invalid", &CloudflareConfig{APIToken: "cf-tok", APITokenVaultPath: "secret/x:api_token"}, ErrCloudflareAPITokenConflict},
		{"service complete valid", &CloudflareConfig{Services: []ServiceIngress{{Hostname: "shield.system.x", Service: "https://10.0.0.9"}}}, nil},
		{"service missing service invalid", &CloudflareConfig{Services: []ServiceIngress{{Hostname: "shield.system.x"}}}, ErrCloudflareServiceIncomplete},
		{"service missing hostname invalid", &CloudflareConfig{Services: []ServiceIngress{{Service: "https://10.0.0.9"}}}, ErrCloudflareServiceIncomplete},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.cfg.Validate()
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
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

	t.Run("services inherited when bloc has none, independent clone", func(t *testing.T) {
		t.Parallel()
		bloc := &Config{Cloudflare: &CloudflareConfig{Origin: "https://10.64.64.38"}}
		defaults := &CloudflareConfig{Services: []ServiceIngress{{Hostname: "shield.system.x", Service: "https://10.0.0.9"}}}
		mergeCloudflareDefaults(bloc, defaults)
		require.Len(t, bloc.Cloudflare.Services, 1)
		assert.Equal(t, "shield.system.x", bloc.Cloudflare.Services[0].Hostname)
		defaults.Services[0].Hostname = "mutated"
		assert.Equal(t, "shield.system.x", bloc.Cloudflare.Services[0].Hostname, "clone must be independent of source")
	})

	t.Run("bloc services win over defaults", func(t *testing.T) {
		t.Parallel()
		bloc := &Config{Cloudflare: &CloudflareConfig{Services: []ServiceIngress{{Hostname: "grafana.system.x", Service: "http://10.0.0.8:3000"}}}}
		defaults := &CloudflareConfig{Services: []ServiceIngress{{Hostname: "shield.system.x", Service: "https://10.0.0.9"}}}
		mergeCloudflareDefaults(bloc, defaults)
		require.Len(t, bloc.Cloudflare.Services, 1)
		assert.Equal(t, "grafana.system.x", bloc.Cloudflare.Services[0].Hostname)
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
