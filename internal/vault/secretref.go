package vault

import "strings"

// ResolveSecretRef returns a secret value from either a literal value or a
// "path:key" vault indirection. A non-empty literal always wins; otherwise the
// ref is split on its last colon and read via safe.GetString.
//
// It returns "" on every miss — empty inputs, a malformed ref, a nil safe, or
// a read error — so callers can treat "" as "unavailable" and warn/skip. The
// last-colon split lets path values themselves contain colons.
func ResolveSecretRef(safe SafeInterface, literal, ref string) string {
	if v := strings.TrimSpace(literal); v != "" {
		return v
	}

	ref = strings.TrimSpace(ref)
	if ref == "" || safe == nil {
		return ""
	}

	idx := strings.LastIndex(ref, ":")
	if idx <= 0 || idx == len(ref)-1 {
		return ""
	}

	val, err := safe.GetString(ref[:idx], ref[idx+1:])
	if err != nil {
		return ""
	}

	return strings.TrimSpace(val)
}
