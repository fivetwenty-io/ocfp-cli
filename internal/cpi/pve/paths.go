package pve

import (
	"fmt"
	"net/url"
	"strings"
)

// buildPVEPath constructs a PVE API path with the node segment path-escaped.
// The node name is passed through url.PathEscape so characters that are
// technically valid in DNS labels but special in URL paths (e.g. "%") are
// encoded safely. Additional path components are appended as-is because they
// are numeric VM IDs or known-safe constant strings.
//
// Example:
//
//	buildPVEPath("pve-node-1", "qemu", "100", "status", "current")
//	→ "/nodes/pve-node-1/qemu/100/status/current"
func buildPVEPath(node string, parts ...string) string {
	base := "/nodes/" + url.PathEscape(node)
	if len(parts) == 0 {
		return base
	}

	var sb strings.Builder
	sb.WriteString(base)

	for _, p := range parts {
		sb.WriteString("/" + p)
	}

	return sb.String()
}

// buildPVEPathf constructs a PVE API path with the node segment path-escaped
// and a printf-style format for the remaining segments.
//
// Example:
//
//	buildPVEPathf("pve-node-1", "qemu/%d/status/current", 100)
//	→ "/nodes/pve-node-1/qemu/100/status/current"
func buildPVEPathf(node, format string, args ...interface{}) string {
	return "/nodes/" + url.PathEscape(node) + "/" + fmt.Sprintf(format, args...)
}
