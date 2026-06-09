package bootstrap

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBastionCloudflareSpec_DisabledWhenNotEnabled(t *testing.T) {
	m := &Manager{config: &config.Config{Cloudflare: &config.CloudflareConfig{}}}
	assert.Nil(t, m.bastionCloudflareSpec())
}

func TestBastionCloudflareSpec_NilWhenNoToken(t *testing.T) {
	m := &Manager{config: &config.Config{Cloudflare: &config.CloudflareConfig{Enabled: boolPtrB(true)}}}
	assert.Nil(t, m.bastionCloudflareSpec())
}

func TestBastionCloudflareSpec_UsesStashedToken(t *testing.T) {
	m := &Manager{
		config:                &config.Config{Cloudflare: &config.CloudflareConfig{Enabled: boolPtrB(true)}},
		cloudflareTunnelToken: "cf-conn",
	}
	spec := m.bastionCloudflareSpec()
	if assert.NotNil(t, spec) {
		assert.Equal(t, "cf-conn", spec.TunnelToken)
	}
}

func boolPtrB(b bool) *bool { return &b }

func TestBuildServiceIngress(t *testing.T) {
	rules := buildServiceIngress([]config.ServiceIngress{
		{Hostname: "shield.system.x", Service: "https://10.0.0.9", NoTLSVerify: boolPtrB(true)},
		{Hostname: "grafana.system.x", Service: "http://10.0.0.8:3000"},
	})
	require.Len(t, rules, 2)
	assert.Equal(t, "shield.system.x", rules[0].Hostname)
	assert.Equal(t, "https://10.0.0.9", rules[0].Service)
	require.NotNil(t, rules[0].OriginRequest)
	assert.True(t, rules[0].OriginRequest.NoTLSVerify)
	assert.Equal(t, "grafana.system.x", rules[1].Hostname)
	assert.Nil(t, rules[1].OriginRequest, "no originRequest when NoTLSVerify is unset")
}

func TestBuildServiceIngress_Empty(t *testing.T) {
	assert.Nil(t, buildServiceIngress(nil))
}
