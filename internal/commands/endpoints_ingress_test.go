package commands

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/stretchr/testify/assert"
)

// TestCollectIngressSection_PerProviderWithOrigin covers all three
// config.ResolveIngressProvider outcomes, each also asserting ORIGIN per the
// amendment: tailscale-with-cf.Origin (tunnel disabled, DNAT to haproxy
// scenario), tailscale-without-cloudflare, cloudflared, and no provider.
func TestCollectIngressSection_PerProviderWithOrigin(t *testing.T) {
	t.Parallel()

	t.Run("tailscale with cf.Origin set (DNAT scenario)", func(t *testing.T) {
		t.Parallel()

		cfg := &config.Config{
			Name:    "test-bloc",
			FQDNs:   &config.FQDNConfig{Base: "ocf.example.lab.internal"},
			Ingress: &config.IngressConfig{Provider: config.IngressProviderTailscale},
			Cloudflare: &config.CloudflareConfig{
				Origin: "https://10.64.64.20",
			},
		}

		section, resolveKeys := collectIngressSection(cfg)

		assert.Len(t, section.Rows, 2)
		assert.Len(t, resolveKeys, 2)

		for i, row := range section.Rows {
			assert.Equal(t, "—", row[2], "row %d EXPECTED TARGET must always be blank on tailscale (R-09)", i)
			assert.Equal(t, originHost(cfg.Cloudflare.Origin), row[3], "row %d ORIGIN must come from cf.Origin alone", i)
		}

		assert.Equal(t, "ocf.example.lab.internal", section.Rows[0][0])
		assert.Equal(t, "*.ocf.example.lab.internal", section.Rows[1][0])
		assert.Equal(t, "", resolveKeys[1], "wildcard record name must get a blank resolve-key")
		assert.Equal(t, "ocf.example.lab.internal", resolveKeys[0])
	})

	t.Run("tailscale with cf == nil", func(t *testing.T) {
		t.Parallel()

		cfg := &config.Config{
			Name:    "test-bloc",
			FQDNs:   &config.FQDNConfig{Base: "ocf.example.lab.internal"},
			Ingress: &config.IngressConfig{Provider: config.IngressProviderTailscale},
		}

		section, resolveKeys := collectIngressSection(cfg)

		assert.Len(t, section.Rows, 2)
		assert.Len(t, resolveKeys, 2)

		for i, row := range section.Rows {
			assert.Equal(t, "—", row[3], "row %d ORIGIN must be blank with no cloudflare config, never an error", i)
		}
	})

	t.Run("cloudflared with full cf config", func(t *testing.T) {
		t.Parallel()

		tunnelEnabled := true
		cfg := &config.Config{
			Name: "test-bloc",
			Cloudflare: &config.CloudflareConfig{
				Enabled:      &tunnelEnabled,
				Origin:       "https://10.64.64.20",
				AppsDomain:   "apps.ocf.example.lab.internal",
				SystemDomain: "system.ocf.example.lab.internal",
				SSHHostname:  "ssh.ocf.example.lab.internal",
				SSHOrigin:    "ssh://10.64.64.37:2222",
				Services: []config.ServiceIngress{
					{Hostname: "shield.system.ocf.example.lab.internal", Service: "https://10.0.0.9"},
				},
			},
		}

		section, resolveKeys := collectIngressSection(cfg)

		assert.Len(t, section.Rows, 4)
		assert.Len(t, resolveKeys, 4)

		for i, row := range section.Rows {
			assert.Equal(t, "—", row[2], "row %d EXPECTED TARGET must always be blank on cloudflared (placeholder removed)", i)
		}

		appsRow := section.Rows[0]
		assert.Equal(t, "*.apps.ocf.example.lab.internal", appsRow[0])
		assert.Equal(t, originHost(cfg.Cloudflare.Origin), appsRow[3])
		assert.Equal(t, "", resolveKeys[0])

		systemRow := section.Rows[1]
		assert.Equal(t, "*.system.ocf.example.lab.internal", systemRow[0])
		assert.Equal(t, originHost(cfg.Cloudflare.Origin), systemRow[3])
		assert.Equal(t, "", resolveKeys[1])

		sshRow := section.Rows[2]
		assert.Equal(t, "ssh.ocf.example.lab.internal", sshRow[0])
		assert.Equal(t, originHost(cfg.Cloudflare.SSHOrigin), sshRow[3])
		assert.Equal(t, "ssh.ocf.example.lab.internal", resolveKeys[2])

		serviceRow := section.Rows[3]
		assert.Equal(t, "shield.system.ocf.example.lab.internal", serviceRow[0])
		assert.Equal(t, "10.0.0.9", serviceRow[3])
		assert.Equal(t, "shield.system.ocf.example.lab.internal", resolveKeys[3])
	})

	t.Run("no ingress provider", func(t *testing.T) {
		t.Parallel()

		cfg := &config.Config{Name: "test-bloc"}

		section, resolveKeys := collectIngressSection(cfg)

		assert.Empty(t, section.Rows)
		assert.Empty(t, resolveKeys)
	})
}

// TestCollectIngressSection_TailscaleNoBaseDomainYieldsZeroRows verifies R-06:
// a tailscale bloc with no fqdns.base gets zero ingress rows, never an error.
func TestCollectIngressSection_TailscaleNoBaseDomainYieldsZeroRows(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name:    "test-bloc",
		Ingress: &config.IngressConfig{Provider: config.IngressProviderTailscale},
	}

	section, resolveKeys := collectIngressSection(cfg)
	assert.Empty(t, section.Rows)
	assert.Empty(t, resolveKeys)
}

// TestCollectIngressSection_Headers verifies the revised Section 3 header
// set: RECORD, TYPE, EXPECTED TARGET, ORIGIN, RESOLVED IP.
func TestCollectIngressSection_Headers(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Name: "test-bloc"}
	section, _ := collectIngressSection(cfg)

	assert.Equal(t, "Ingress Records", section.Title)
	assert.Equal(t, []string{"RECORD", "TYPE", "EXPECTED TARGET", "ORIGIN", "RESOLVED IP"}, section.Headers)
}

// TestCollectBastionSection_TailscaleShowsHostname verifies the bastion
// tailnet hostname row appears (explicit tailscale.hostname when set) only
// when the resolved ingress provider is tailscale, alongside the always-
// present Bastion IP row.
func TestCollectBastionSection_TailscaleShowsHostname(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name:      "test-bloc",
		BastionIP: "10.64.64.5",
		Ingress:   &config.IngressConfig{Provider: config.IngressProviderTailscale},
		Tailscale: &config.TailscaleConfig{Hostname: "custom-bastion-host"},
	}

	section := collectBastionSection(cfg)

	assert.Equal(t, "Bastion", section.Title)
	assert.Equal(t, []string{"NAME", "VALUE"}, section.Headers)
	assert.Equal(t, [][]string{
		{"Bastion IP", "10.64.64.5"},
		{"Bastion Tailnet Hostname", "custom-bastion-host"},
	}, section.Rows)
}

// TestCollectBastionSection_TailscaleDefaultsHostnameToBlocNameSuffix
// verifies the fallback hostname (<bloc>-bastion) when tailscale.hostname is
// unset, mirroring bootstrap's bastionTailnetHostname.
func TestCollectBastionSection_TailscaleDefaultsHostnameToBlocNameSuffix(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Name:    "acme-mgmt",
		Ingress: &config.IngressConfig{Provider: config.IngressProviderTailscale},
	}

	section := collectBastionSection(cfg)

	assert.Equal(t, [][]string{
		{"Bastion IP", "—"},
		{"Bastion Tailnet Hostname", "acme-mgmt-bastion"},
	}, section.Rows)
}

// TestCollectBastionSection_NonTailscaleOmitsHostnameRow verifies the
// hostname row is entirely absent (not blank) for any non-tailscale ingress
// provider, including no provider at all.
func TestCollectBastionSection_NonTailscaleOmitsHostnameRow(t *testing.T) {
	t.Parallel()

	tunnelEnabled := true
	cfg := &config.Config{
		Name:       "test-bloc",
		BastionIP:  "10.64.64.5",
		Cloudflare: &config.CloudflareConfig{Enabled: &tunnelEnabled},
	}

	section := collectBastionSection(cfg)

	assert.Equal(t, [][]string{
		{"Bastion IP", "10.64.64.5"},
	}, section.Rows)
}
