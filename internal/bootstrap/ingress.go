package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cloudflare"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// tailnetStatusJSON returns `tailscale status --json` output from the machine
// running bootstrap (which must be joined to the tailnet). Package var so
// tests can stub it without shelling out.
var tailnetStatusJSON = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
}

const (
	tailnetIPPollInterval = 10 * time.Second
	tailnetIPPollTimeout  = 5 * time.Minute
)

// ConfigureIngressDNS points <base> and *.<base> A records at the bastion's
// tailnet IP when the resolved ingress provider is tailscale. Runs after the
// bastion exists (it must have joined the tailnet). Soft-skips on any missing
// prerequisite — ingress must never fail bootstrap.
func (m *Manager) ConfigureIngressDNS(ctx context.Context) error {
	if m.config == nil || config.ResolveIngressProvider(m.config) != config.IngressProviderTailscale {
		logger.Infof("Tailscale ingress not active; skipping DNS records")

		return nil
	}

	cf := m.config.Cloudflare
	if cf == nil || cf.Zone == "" {
		logger.Warnf("Tailscale ingress: cloudflare.zone unset; skipping DNS records")

		return nil
	}

	base := m.ingressBaseDomain()
	if base == "" {
		logger.Warnf("Tailscale ingress: fqdns.base unset; skipping DNS records")

		return nil
	}

	token := m.resolveBastionCloudflareAPIToken()
	if token == "" {
		logger.Warnf("Tailscale ingress: Cloudflare API token unavailable; skipping DNS records")

		return nil
	}

	ip, err := m.discoverBastionTailnetIP(ctx)
	if err != nil {
		logger.Warnf("Tailscale ingress: %v; skipping DNS records", err)

		return nil
	}

	client := cloudflare.NewClient(token, nil)

	_, zoneID, err := client.ResolveAccountAndZone(ctx, cf.Zone)
	if err != nil {
		return fmt.Errorf("ingress dns resolve zone: %w", err)
	}

	for _, name := range ingressRecordNames(base) {
		err := client.UpsertA(ctx, zoneID, name, ip)
		if err != nil {
			return fmt.Errorf("ingress dns %s: %w", name, err)
		}
	}

	if safe := m.tailscaleSafe(); safe != nil {
		vp := "secret/config/" + m.options.BlocName + "/ingress"

		err := safe.SetMultiple(vp, map[string]interface{}{
			"provider":           config.IngressProviderTailscale,
			"bastion_tailnet_ip": ip,
			"base":               base,
		})
		if err != nil {
			logger.Warnf("Tailscale ingress: persist to vault: %v", err)
		}
	}

	logger.Infof("Tailscale ingress DNS ready: %s and *.%s -> %s", base, base, ip)

	return nil
}

// ingressBaseDomain returns the bloc's fqdns.base, the apex the ingress
// wildcard hangs off.
func (m *Manager) ingressBaseDomain() string {
	if m.config == nil || m.config.FQDNs == nil {
		return ""
	}

	return strings.TrimSpace(m.config.FQDNs.Base)
}

// ingressRecordNames returns the DNS names tailscale ingress manages for a
// base domain: the apex and its wildcard.
func ingressRecordNames(base string) []string {
	return []string{base, "*." + base}
}

// discoverBastionTailnetIP polls the local tailscale CLI until the bastion
// hostname appears with an IPv4 tailnet address, or the poll window expires.
func (m *Manager) discoverBastionTailnetIP(ctx context.Context) (string, error) {
	hostname := m.bastionTailnetHostname()

	deadline := time.Now().Add(tailnetIPPollTimeout)

	for {
		out, err := tailnetStatusJSON(ctx)
		if err == nil {
			if ip := tailnetIPForHost(out, hostname); ip != "" {
				return ip, nil
			}
		}

		if time.Now().After(deadline) {
			return "", fmt.Errorf("bastion %q not found on tailnet within %s", hostname, tailnetIPPollTimeout)
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(tailnetIPPollInterval):
		}
	}
}

// bastionTailnetHostname mirrors the hostname bastionTailscaleSpec sends:
// explicit tailscale.hostname, else "<bloc>-bastion".
func (m *Manager) bastionTailnetHostname() string {
	if m.config != nil && m.config.Tailscale != nil && strings.TrimSpace(m.config.Tailscale.Hostname) != "" {
		return strings.TrimSpace(m.config.Tailscale.Hostname)
	}

	return m.options.BlocName + "-bastion"
}

// tailnetIPForHost extracts the first IPv4 tailnet address for hostname from
// `tailscale status --json` output. Empty when absent.
func tailnetIPForHost(statusJSON []byte, hostname string) string {
	var status struct {
		Self struct {
			HostName     string   `json:"HostName"`
			TailscaleIPs []string `json:"TailscaleIPs"`
		} `json:"Self"`
		Peer map[string]struct {
			HostName     string   `json:"HostName"`
			TailscaleIPs []string `json:"TailscaleIPs"`
		} `json:"Peer"`
	}

	if err := json.Unmarshal(statusJSON, &status); err != nil {
		return ""
	}

	firstV4 := func(ips []string) string {
		for _, ip := range ips {
			if strings.Contains(ip, ".") {
				return ip
			}
		}

		return ""
	}

	if status.Self.HostName == hostname {
		return firstV4(status.Self.TailscaleIPs)
	}

	for _, p := range status.Peer {
		if p.HostName == hostname {
			return firstV4(p.TailscaleIPs)
		}
	}

	return ""
}
