package cloudflare

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureTunnel_GetsExisting(t *testing.T) {
	t.Parallel()
	f := &fakeDoer{responses: map[string]string{
		"GET /client/v4/accounts/acct/cfd_tunnel":             `{"success":true,"result":[{"id":"tun-1","name":"ocfp-lab-wayne"}]}`,
		"GET /client/v4/accounts/acct/cfd_tunnel/tun-1/token": `{"success":true,"result":"tok-eyJ"}`,
	}}
	c := NewClient("t", f)
	tun, err := c.EnsureTunnel(context.Background(), "acct", "ocfp-lab-wayne")
	require.NoError(t, err)
	assert.Equal(t, "tun-1", tun.ID)
	assert.Equal(t, "tok-eyJ", tun.Token)
}

func TestEnsureTunnel_CreatesWhenAbsent(t *testing.T) {
	t.Parallel()
	f := &fakeDoer{responses: map[string]string{
		"GET /client/v4/accounts/acct/cfd_tunnel":               `{"success":true,"result":[]}`,
		"POST /client/v4/accounts/acct/cfd_tunnel":              `{"success":true,"result":{"id":"tun-new","name":"ocfp-lab-wayne"}}`,
		"GET /client/v4/accounts/acct/cfd_tunnel/tun-new/token": `{"success":true,"result":"tok-new"}`,
	}}
	c := NewClient("t", f)
	tun, err := c.EnsureTunnel(context.Background(), "acct", "ocfp-lab-wayne")
	require.NoError(t, err)
	assert.Equal(t, "tun-new", tun.ID)
	assert.Equal(t, "tok-new", tun.Token)
}

func TestBuildIngress(t *testing.T) {
	t.Parallel()
	ing := BuildIngress(IngressParams{
		AppsDomain:       "apps.ocf.wayne.lab.fivetwenty.io",
		SystemDomain:     "system.ocf.wayne.lab.fivetwenty.io",
		SSHHostname:      "ssh.system.ocf.wayne.lab.fivetwenty.io",
		Origin:           "https://10.64.64.38",
		SSHOrigin:        "ssh://10.64.64.37:2222",
		OriginServerName: "api.system.ocf.wayne.lab.fivetwenty.io",
	})
	// 4 rules, ssh-first (specific before the *.system wilddcard): ssh, apps, system, catch-all
	require.Len(t, ing, 4)
	assert.Equal(t, "ssh.system.ocf.wayne.lab.fivetwenty.io", ing[0].Hostname)
	assert.Equal(t, "ssh://10.64.64.37:2222", ing[0].Service)
	assert.Equal(t, "*.apps.ocf.wayne.lab.fivetwenty.io", ing[1].Hostname)
	assert.True(t, ing[1].OriginRequest.NoTLSVerify)
	assert.Equal(t, "*.system.ocf.wayne.lab.fivetwenty.io", ing[2].Hostname)
	assert.Equal(t, "api.system.ocf.wayne.lab.fivetwenty.io", ing[2].OriginRequest.OriginServerName)
	assert.Equal(t, "http_status:404", ing[3].Service)
	assert.Empty(t, ing[3].Hostname)
}

// TestBuildIngress_Services — explicit service rules must precede the *.system
// wildcard (cloudflared first-match) so a hostname like shield.system.<domain>
// routes to its own origin, not the gorouter/haproxy.
func TestBuildIngress_Services(t *testing.T) {
	t.Parallel()
	ing := BuildIngress(IngressParams{
		AppsDomain:        "apps.ocf.wayne.lab.fivetwenty.io",
		SystemDomain:      "system.ocf.wayne.lab.fivetwenty.io",
		SSHHostname:       "ssh.system.ocf.wayne.lab.fivetwenty.io",
		Origin:            "https://10.64.68.13:443",
		SSHOrigin:         "ssh://10.64.68.13:2222",
		OriginNoTLSVerify: true,
		Services: []IngressRule{
			{Hostname: "shield.system.ocf.wayne.lab.fivetwenty.io", Service: "https://10.64.68.9", OriginRequest: &OriginRequest{NoTLSVerify: true}},
			{Hostname: "grafana.system.ocf.wayne.lab.fivetwenty.io", Service: "http://10.64.68.8:3000"},
		},
	})
	// services(2), ssh, apps, system, catch-all
	require.Len(t, ing, 6)
	assert.Equal(t, "shield.system.ocf.wayne.lab.fivetwenty.io", ing[0].Hostname)
	assert.True(t, ing[0].OriginRequest.NoTLSVerify)
	assert.Equal(t, "grafana.system.ocf.wayne.lab.fivetwenty.io", ing[1].Hostname)
	assert.Equal(t, "ssh.system.ocf.wayne.lab.fivetwenty.io", ing[2].Hostname)
	assert.Equal(t, "*.apps.ocf.wayne.lab.fivetwenty.io", ing[3].Hostname)
	assert.Equal(t, "*.system.ocf.wayne.lab.fivetwenty.io", ing[4].Hostname)
	assert.Equal(t, "http_status:404", ing[5].Service)
}

// TestBuildIngress_OriginNoTLSVerify — when the origin uses a self-signed cert
// (e.g. the PVE lab haproxy), OriginNoTLSVerify must disable TLS verification on
// the *.system rule too (the default verifies via OriginServerName, which 502s
// against a self-signed origin). *.apps is always noTLSVerify.
func TestBuildIngress_OriginNoTLSVerify(t *testing.T) {
	t.Parallel()
	ing := BuildIngress(IngressParams{
		AppsDomain:        "apps.ocf.wayne.lab.fivetwenty.io",
		SystemDomain:      "system.ocf.wayne.lab.fivetwenty.io",
		Origin:            "https://10.64.68.13:443",
		OriginServerName:  "api.system.ocf.wayne.lab.fivetwenty.io",
		OriginNoTLSVerify: true,
	})
	// apps, system, catch-all (no ssh configured here)
	require.Len(t, ing, 3)
	assert.True(t, ing[0].OriginRequest.NoTLSVerify, "*.apps always noTLSVerify")
	assert.Equal(t, "*.system.ocf.wayne.lab.fivetwenty.io", ing[1].Hostname)
	assert.True(t, ing[1].OriginRequest.NoTLSVerify, "*.system noTLSVerify when OriginNoTLSVerify set")
	assert.Empty(t, ing[1].OriginRequest.OriginServerName, "no cert verify name when skipping verify")
}
