package commands

import (
	"net/url"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
)

// originHost extracts the bare host from a scheme://host[:port] config
// string (e.g. a cf.Origin, cf.SSHOrigin, or services[].Service value),
// dropping any scheme and port. Returns "" on empty input or any parse
// failure — never an error, since every caller only wants a best-effort
// display value for the ORIGIN column.
//
//nolint:unused // wired into buildEndpointsTable in a follow-up commit (Task 11)
func originHost(serviceURL string) string {
	if serviceURL == "" {
		return ""
	}

	parsed, err := url.Parse(serviceURL)
	if err != nil {
		return ""
	}

	return parsed.Hostname()
}

// cfExactHostnameOrigins builds the exact-match hostname-to-origin map used
// as rung O1 of the ORIGIN fallback in Sections 1-3: for every configured
// Cloudflare hostname (services[] entries, plus the SSH route when
// configured), the bare origin host it terminates at. Always returns a
// non-nil, possibly empty map — cf == nil is not an error, it means no
// Cloudflare config at all, i.e. no exact routes.
//
//nolint:unused // consumed by Section 1/2/3 ORIGIN builders, landing in follow-up commits
func cfExactHostnameOrigins(cf *config.CloudflareConfig) map[string]string {
	origins := make(map[string]string)

	if cf == nil {
		return origins
	}

	for _, svc := range cf.Services {
		if svc.Hostname == "" {
			continue
		}

		origins[svc.Hostname] = originHost(svc.Service)
	}

	if cf.SSHHostname != "" {
		origins[cf.SSHHostname] = originHost(cf.SSHOrigin)
	}

	return origins
}

// collectCloudflareSection builds Section 2 (Cloudflare Service Routes): one
// row per configured route (the *.apps and *.system wildcards, the SSH
// route when configured, and every services[] entry), showing the raw
// configured SERVICE URL alongside its extracted bare-host ORIGIN. cf == nil
// returns a 0-row section, never an error — a bloc with no Cloudflare config
// simply has nothing to report here.
//
// No EXPECTED IP column: for every row here, a plain DNS lookup of HOSTNAME
// never directly returns the origin IP (cloudflared: CNAME to the tunnel;
// tailscale: no per-service record is ever created for these hostnames at
// all), so asserting that fact as a column would always be misleading.
//
//nolint:unused // wired into buildEndpointsTable in a follow-up commit (Task 11)
func collectCloudflareSection(cf *config.CloudflareConfig) (ui.Section, []string) {
	section := ui.Section{
		Title:   "Cloudflare Service Routes",
		Headers: []string{"KIND", "HOSTNAME", "SERVICE URL", "ORIGIN", "RESOLVED IP"},
		Rows:    [][]string{},
	}

	if cf == nil {
		return section, nil
	}

	var resolveKeys []string

	appendRow := func(kind, hostname, rawURL string) {
		section.Rows = append(section.Rows, []string{
			kind,
			hostname,
			dashIfEmpty(rawURL),
			dashIfEmpty(originHost(rawURL)),
			"—",
		})
		resolveKeys = append(resolveKeys, hostname)
	}

	if cf.AppsDomain != "" {
		appendRow("apps wildcard", "*."+cf.AppsDomain, cf.Origin)
	}

	if cf.SystemDomain != "" {
		appendRow("system wildcard", "*."+cf.SystemDomain, cf.Origin)
	}

	if cf.SSHHostname != "" {
		appendRow("ssh", cf.SSHHostname, cf.SSHOrigin)
	}

	for _, svc := range cf.Services {
		appendRow("service", svc.Hostname, svc.Service)
	}

	return section, resolveKeys
}
