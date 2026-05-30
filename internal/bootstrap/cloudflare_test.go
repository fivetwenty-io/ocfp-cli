package bootstrap

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
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
