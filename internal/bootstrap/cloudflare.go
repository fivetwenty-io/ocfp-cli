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

	accountID, zoneID, err := client.ResolveAccountAndZone(cf.Zone)
	if err != nil {
		return fmt.Errorf("cloudflare resolve account/zone: %w", err)
	}
	tun, err := client.EnsureTunnel(ctx, accountID, name)
	if err != nil {
		return fmt.Errorf("cloudflare ensure tunnel: %w", err)
	}

	ingress := cloudflare.BuildIngress(cloudflare.IngressParams{
		AppsDomain:       cf.AppsDomain,
		SystemDomain:     cf.SystemDomain,
		SSHHostname:      cf.SSHHostname,
		Origin:           cf.Origin,
		SSHOrigin:        cf.SSHOrigin,
		OriginServerName: firstNonEmpty(cf.OriginServerName, "api."+cf.SystemDomain),
	})
	if err := client.PutTunnelConfig(ctx, accountID, tun.ID, ingress); err != nil {
		return fmt.Errorf("cloudflare put ingress: %w", err)
	}

	target := tun.ID + ".cfargotunnel.com"
	names := []string{"*." + cf.AppsDomain, "*." + cf.SystemDomain}
	if cf.SSHHostname != "" {
		names = append(names, cf.SSHHostname)
	}
	for _, dnsName := range names {
		if err := client.UpsertCNAME(ctx, zoneID, dnsName, target); err != nil {
			return fmt.Errorf("cloudflare dns %s: %w", dnsName, err)
		}
	}

	if safe := m.tailscaleSafe(); safe != nil {
		vp := "secret/config/" + m.options.BlocName + "/cloudflare"
		_ = safe.Set(vp, "tunnel_id", tun.ID)
		_ = safe.Set(vp, "tunnel_token", tun.Token)
		_ = safe.Set(vp, "account_id", accountID)
	}
	m.cloudflareTunnelToken = strings.TrimSpace(tun.Token)
	logger.Infof("Cloudflare tunnel %q ready (id %s)", name, tun.ID)
	return nil
}
