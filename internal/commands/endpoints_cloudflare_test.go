package commands

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
)

// TestOriginHost_StripsSchemeAndPort verifies originHost extracts the bare
// host from a scheme://host[:port] config string, returning blank on any
// parse failure or empty input.
func TestOriginHost_StripsSchemeAndPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "https no port", in: "https://10.64.64.20", want: "10.64.64.20"},
		{name: "ssh with port", in: "ssh://10.64.64.37:2222", want: "10.64.64.37"},
		{name: "empty", in: "", want: ""},
		{name: "not a url", in: "not a url", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := originHost(tt.in)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestCfExactHostnameOrigins_NilCloudflareReturnsEmptyMap verifies a nil
// CloudflareConfig yields an empty, non-nil map rather than an error or a
// nil map.
func TestCfExactHostnameOrigins_NilCloudflareReturnsEmptyMap(t *testing.T) {
	t.Parallel()

	got := cfExactHostnameOrigins(nil)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

// TestCfExactHostnameOrigins_BuildsFromServicesAndSSH verifies the map
// contains one entry per cf.Services[] hostname plus, when set, the SSH
// route — each mapped to its origin's bare host.
func TestCfExactHostnameOrigins_BuildsFromServicesAndSSH(t *testing.T) {
	t.Parallel()

	cf := &config.CloudflareConfig{
		SSHHostname: "ssh.ocf.example.lab.internal",
		SSHOrigin:   "ssh://10.64.64.37:2222",
		Services: []config.ServiceIngress{
			{Hostname: "shield.system.ocf.example.lab.internal", Service: "https://10.0.0.9"},
		},
	}

	got := cfExactHostnameOrigins(cf)

	assert.Equal(t, map[string]string{
		"shield.system.ocf.example.lab.internal": "10.0.0.9",
		"ssh.ocf.example.lab.internal":           "10.64.64.37",
	}, got)
}

// TestCfExactHostnameOrigins_SkipsBlankSSHHostname verifies the SSH route is
// omitted from the map entirely when cf.SSHHostname is blank, even if
// cf.SSHOrigin is set.
func TestCfExactHostnameOrigins_SkipsBlankSSHHostname(t *testing.T) {
	t.Parallel()

	cf := &config.CloudflareConfig{
		SSHOrigin: "ssh://10.64.64.37:2222",
	}

	got := cfExactHostnameOrigins(cf)
	assert.Empty(t, got)
}

// TestCollectCloudflareSection_FullConfig verifies Section 2 renders one row
// per configured route (apps wildcard, system wildcard, ssh, one service),
// with SERVICE URL carrying the raw configured string and ORIGIN carrying
// its extracted bare host.
func TestCollectCloudflareSection_FullConfig(t *testing.T) {
	t.Parallel()

	cf := &config.CloudflareConfig{
		Origin:       "https://10.64.64.20",
		AppsDomain:   "apps.ocf.example.lab.internal",
		SystemDomain: "system.ocf.example.lab.internal",
		SSHHostname:  "ssh.ocf.example.lab.internal",
		SSHOrigin:    "ssh://10.64.64.37:2222",
		Services: []config.ServiceIngress{
			{Hostname: "shield.system.ocf.example.lab.internal", Service: "https://10.0.0.9"},
		},
	}

	section, resolveKeys := collectCloudflareSection(cf)

	assert.Equal(t, "Cloudflare Service Routes", section.Title)
	assert.Equal(t, []string{"KIND", "HOSTNAME", "SERVICE URL", "ORIGIN", "RESOLVED IP"}, section.Headers)
	assert.Len(t, section.Rows, 4)
	assert.Len(t, resolveKeys, 4)

	appsRow := section.Rows[0]
	assert.Equal(t, "apps wildcard", appsRow[0])
	assert.Equal(t, "*.apps.ocf.example.lab.internal", appsRow[1])
	assert.Equal(t, cf.Origin, appsRow[2])
	assert.Equal(t, originHost(cf.Origin), appsRow[3])

	systemRow := section.Rows[1]
	assert.Equal(t, "system wildcard", systemRow[0])
	assert.Equal(t, "*.system.ocf.example.lab.internal", systemRow[1])
	assert.Equal(t, cf.Origin, systemRow[2])
	assert.Equal(t, originHost(cf.Origin), systemRow[3])

	sshRow := section.Rows[2]
	assert.Equal(t, "ssh", sshRow[0])
	assert.Equal(t, cf.SSHHostname, sshRow[1])
	assert.Equal(t, cf.SSHOrigin, sshRow[2])
	assert.Equal(t, originHost(cf.SSHOrigin), sshRow[3])

	serviceRow := section.Rows[3]
	assert.Equal(t, "service", serviceRow[0])
	assert.Equal(t, "shield.system.ocf.example.lab.internal", serviceRow[1])
	assert.Equal(t, "https://10.0.0.9", serviceRow[2])
	assert.Equal(t, "10.0.0.9", serviceRow[3])
}

// TestCollectCloudflareSection_NilCloudflareReturnsZeroRows verifies a bloc
// with no Cloudflare config produces a 0-row section and no error.
func TestCollectCloudflareSection_NilCloudflareReturnsZeroRows(t *testing.T) {
	t.Parallel()

	section, resolveKeys := collectCloudflareSection(nil)

	assert.Equal(t, "Cloudflare Service Routes", section.Title)
	assert.Empty(t, section.Rows)
	assert.Empty(t, resolveKeys)
}

// TestCFExactHostnameOrigins_FirstMatchWins verifies the map mirrors the
// order cloudflared actually evaluates ingress rules in. BuildIngress
// (internal/cloudflare/tunnel.go:110-113) emits services[] first, then the
// SSH route, and cloudflared takes the first matching rule — so a duplicate
// services[] hostname resolves to the earlier entry, and a services[] entry
// colliding with ssh_hostname beats the SSH route rather than losing to it.
// Nothing validates either collision away, so both are reachable configs.
func TestCFExactHostnameOrigins_FirstMatchWins(t *testing.T) {
	t.Parallel()

	cf := &config.CloudflareConfig{
		SSHHostname: "collide.example.lab.internal",
		SSHOrigin:   "ssh://10.0.0.99:22",
		Services: []config.ServiceIngress{
			{Hostname: "dup.example.lab.internal", Service: "https://10.0.0.1"},
			{Hostname: "dup.example.lab.internal", Service: "https://10.0.0.2"},
			{Hostname: "collide.example.lab.internal", Service: "https://10.0.0.3"},
		},
	}

	got := cfExactHostnameOrigins(cf)

	assert.Equal(t, "10.0.0.1", got["dup.example.lab.internal"])
	assert.Equal(t, "10.0.0.3", got["collide.example.lab.internal"])
}
