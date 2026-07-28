package commands

import (
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
)

// collectIngressSection builds Section 3 (Ingress Records): the DNS record
// set a bloc's ingress provider is responsible for, branched on
// config.ResolveIngressProvider, now with an ORIGIN column showing where
// each record's traffic terminates today (Section 1/2's same exact-route
// map, plus the tier-agnostic wildcard rule for cloudflared/tailscale's own
// wildcard-suffix records). EXPECTED TARGET is always "—": the tailscale
// tailnet IP would need a local `tailscale` CLI shell-out (R-09, never
// performed here), and the cloudflared tunnel ID would need a vault read
// (R-10/D-01, out of scope). An unresolved/empty provider returns a 0-row
// section, never an error.
//
//nolint:unused // wired into buildEndpointsTable in a follow-up commit (Task 11)
func collectIngressSection(cfg *config.Config) (ui.Section, []string) {
	section := ui.Section{
		Title:   "Ingress Records",
		Headers: []string{"RECORD", "TYPE", "EXPECTED TARGET", "ORIGIN", "RESOLVED IP"},
		Rows:    [][]string{},
	}

	if cfg == nil {
		return section, nil
	}

	switch config.ResolveIngressProvider(cfg) {
	case config.IngressProviderTailscale:
		return collectTailscaleIngressRows(cfg, section)
	case config.IngressProviderCloudflared:
		return collectCloudflaredIngressRows(cfg, section)
	default:
		return section, nil
	}
}

// collectTailscaleIngressRows renders the apex and wildcard A records
// tailscale ingress manages (mirrors bootstrap.ingressRecordNames), gated on
// fqdns.base being set (R-06). ORIGIN comes from cf.Origin alone — the
// tunnel can be disabled while the field still carries the true DNAT target
// (a bastion kernel DNAT to the CF haproxy static), so gating on
// config.CloudflareEnabled would hide a real, locally-known fact.
//
//nolint:unused // called from collectIngressSection, wired in a follow-up commit (Task 11)
func collectTailscaleIngressRows(cfg *config.Config, section ui.Section) (ui.Section, []string) {
	if cfg.FQDNs == nil || cfg.FQDNs.Base == "" {
		return section, nil
	}

	origin := "—"
	if cfg.Cloudflare != nil && cfg.Cloudflare.Origin != "" {
		origin = originHost(cfg.Cloudflare.Origin)
	}

	base := cfg.FQDNs.Base
	names := []string{base, "*." + base}

	var resolveKeys []string

	for _, name := range names {
		section.Rows = append(section.Rows, []string{name, "A", "—", dashIfEmpty(origin), "—"})
		resolveKeys = append(resolveKeys, resolveKeyForRecord(name))
	}

	return section, resolveKeys
}

// collectCloudflaredIngressRows renders the cloudflared tunnel's ingress
// rules: the *.apps and *.system wildcards (tier-agnostic — BuildIngress
// routes both to the same origin, cloudflare/tunnel.go:115-126), the SSH
// route when configured, and every services[] entry. ORIGIN for the
// wildcard rows is cf.Origin unconditionally; ORIGIN for the SSH/service
// rows reuses the same exact-hostname map Section 2 builds from (Task 3).
//
//nolint:unused // called from collectIngressSection, wired in a follow-up commit (Task 11)
func collectCloudflaredIngressRows(cfg *config.Config, section ui.Section) (ui.Section, []string) {
	cf := cfg.Cloudflare
	if cf == nil {
		return section, nil
	}

	exact := cfExactHostnameOrigins(cf)

	var resolveKeys []string

	appendRow := func(name, origin string) {
		section.Rows = append(section.Rows, []string{name, "CNAME", "—", dashIfEmpty(origin), "—"})
		resolveKeys = append(resolveKeys, resolveKeyForRecord(name))
	}

	if cf.AppsDomain != "" {
		appendRow("*."+cf.AppsDomain, originHost(cf.Origin))
	}

	if cf.SystemDomain != "" {
		appendRow("*."+cf.SystemDomain, originHost(cf.Origin))
	}

	if cf.SSHHostname != "" {
		appendRow(cf.SSHHostname, exact[cf.SSHHostname])
	}

	for _, svc := range cf.Services {
		appendRow(svc.Hostname, exact[svc.Hostname])
	}

	return section, resolveKeys
}

// collectBastionSection builds Section 4 (Bastion): the bastion's allocated
// IP (always present, dashed when blank) and, only when the resolved
// ingress provider is tailscale, its tailnet hostname. No ORIGIN/RESOLVED
// column here and no network activity ever — the bastion is the entry
// point itself, not a backend behind a wildcard, and its tailnet hostname
// is a local config-derived value, never looked up.
//
//nolint:unused // wired into buildEndpointsTable in a follow-up commit (Task 11)
func collectBastionSection(cfg *config.Config) ui.Section {
	section := ui.Section{
		Title:   "Bastion",
		Headers: []string{"NAME", "VALUE"},
		Rows: [][]string{
			{"Bastion IP", dashIfEmpty(cfg.BastionIP)},
		},
	}

	if config.ResolveIngressProvider(cfg) == config.IngressProviderTailscale {
		section.Rows = append(section.Rows, []string{"Bastion Tailnet Hostname", bastionTailnetHostname(cfg)})
	}

	return section
}

// bastionTailnetHostname mirrors bootstrap.Manager.bastionTailnetHostname:
// explicit tailscale.hostname when set, else "<bloc>-bastion". Inlined here
// rather than exported from internal/bootstrap, since this command has no
// other reason to depend on that package.
//
//nolint:unused // called from collectBastionSection, wired in a follow-up commit (Task 11)
func bastionTailnetHostname(cfg *config.Config) string {
	if cfg.Tailscale != nil && strings.TrimSpace(cfg.Tailscale.Hostname) != "" {
		return strings.TrimSpace(cfg.Tailscale.Hostname)
	}

	return cfg.Name + "-bastion"
}

// resolveKeyForRecord returns the hostname resolveAll should look up for a
// record name, or "" for a wildcard name (containing "*"), which can never
// be looked up directly.
//
//nolint:unused // called from both provider branches, wired in a follow-up commit (Task 11)
func resolveKeyForRecord(name string) string {
	if strings.Contains(name, "*") {
		return ""
	}

	return name
}
