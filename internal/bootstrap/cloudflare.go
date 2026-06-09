package bootstrap

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/cloudflare"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// CreateCloudflareTunnel ensures the per-bloc tunnel exists, pushes ingress,
// upserts the wildcard CNAMEs, persists the tunnel id/token to vault, and
// stashes the connector token on the Manager for the bastion SMBIOS payload.
// No-op (soft) when cloudflare is disabled or the API token is unavailable.
func (m *Manager) CreateCloudflareTunnel(ctx context.Context) error {
	if m.config == nil || !config.CloudflareEnabled(m.config.Cloudflare) {
		logger.Infof("Cloudflare tunnel disabled; skipping")
		return nil
	}
	cf := m.config.Cloudflare

	token := m.resolveBastionCloudflareAPIToken()
	if token == "" {
		logger.Warnf("Cloudflare enabled but API token unavailable; skipping tunnel")
		return nil
	}

	name := firstNonEmpty(cf.TunnelName, "ocfp-lab-"+m.options.BlocName)
	client := cloudflare.NewClient(token, nil)

	accountID, zoneID, err := client.ResolveAccountAndZone(ctx, cf.Zone)
	if err != nil {
		return fmt.Errorf("cloudflare resolve account/zone: %w", err)
	}
	tun, err := client.EnsureTunnel(ctx, accountID, name)
	if err != nil {
		return fmt.Errorf("cloudflare ensure tunnel: %w", err)
	}

	serviceRules := buildServiceIngress(cf.Services)
	ingress := cloudflare.BuildIngress(cloudflare.IngressParams{
		AppsDomain:        cf.AppsDomain,
		SystemDomain:      cf.SystemDomain,
		SSHHostname:       cf.SSHHostname,
		Origin:            cf.Origin,
		SSHOrigin:         cf.SSHOrigin,
		OriginServerName:  firstNonEmpty(cf.OriginServerName, "api."+cf.SystemDomain),
		OriginNoTLSVerify: cf.OriginNoTLSVerify != nil && *cf.OriginNoTLSVerify,
		Services:          serviceRules,
	})
	if err := client.PutTunnelConfig(ctx, accountID, tun.ID, ingress); err != nil {
		return fmt.Errorf("cloudflare put ingress: %w", err)
	}

	target := tun.ID + ".cfargotunnel.com"
	names := []string{"*." + cf.AppsDomain, "*." + cf.SystemDomain}
	if cf.SSHHostname != "" {
		names = append(names, cf.SSHHostname)
	}
	// A proxied CNAME per service hostname (idempotent; a specific record under
	// an existing wildcard simply shadows it with the same tunnel target).
	for _, s := range cf.Services {
		names = append(names, s.Hostname)
	}
	for _, dnsName := range names {
		if err := client.UpsertCNAME(ctx, zoneID, dnsName, target); err != nil {
			return fmt.Errorf("cloudflare dns %s: %w", dnsName, err)
		}
	}

	safe := m.tailscaleSafe()
	if safe == nil {
		return fmt.Errorf("cloudflare: vault unavailable, cannot persist tunnel identifiers")
	}
	vp := "secret/config/" + m.options.BlocName + "/cloudflare"
	if err := safe.SetMultiple(vp, map[string]interface{}{
		"tunnel_id":    tun.ID,
		"tunnel_token": tun.Token,
		"account_id":   accountID,
	}); err != nil {
		return fmt.Errorf("cloudflare: persist tunnel identifiers to vault: %w", err)
	}
	m.cloudflareTunnelToken = strings.TrimSpace(tun.Token)
	logger.Infof("Cloudflare tunnel %q ready (id %s)", name, tun.ID)
	return nil
}

// buildServiceIngress converts the configured extra services into cloudflared
// ingress rules. An originRequest is attached only when TLS verification is
// disabled (self-signed origin); nil otherwise. Returns nil for an empty list.
func buildServiceIngress(services []config.ServiceIngress) []cloudflare.IngressRule {
	if len(services) == 0 {
		return nil
	}
	rules := make([]cloudflare.IngressRule, 0, len(services))
	for _, s := range services {
		rule := cloudflare.IngressRule{Hostname: s.Hostname, Service: s.Service}
		if s.NoTLSVerify != nil && *s.NoTLSVerify {
			rule.OriginRequest = &cloudflare.OriginRequest{NoTLSVerify: true}
		}
		rules = append(rules, rule)
	}
	return rules
}
