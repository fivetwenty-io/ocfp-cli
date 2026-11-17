package provision

// blocSafeTargetSnippet resolves the bloc's own safe target into
// BLOC_SAFE_TARGET and defines ocfp_bloc_safe, which runs safe pinned to that
// target with -T. Each line is prefixed with indent.
//
// safe's current target belongs to the operator, and ocfp registers its own
// targets without selecting them, so a bare safe read on the bastion would
// reach whichever vault the operator chose, or none. The bloc vault's
// <bloc>-mgmt target wins once it is registered, and the bloc's
// <bloc>-inception target holds the bloc's secrets until then, the same
// order the CLI's own reads use. Both are matched whole, as fixed strings,
// so a sibling bloc's target never counts. With neither registered,
// ocfp_bloc_safe refuses instead of falling back to the current target.
//
// The script may define ocfp_bloc_safe more than once when generated parts
// are joined, which is harmless because every definition is the same.
func blocSafeTargetSnippet(indent string) []string {
	lines := []string{
		"# safe's current target belongs to the operator, so every vault read",
		"# here names the bloc's own target with -T: the bloc vault's mgmt",
		"# target once it is registered, the inception target until then. Names",
		"# are matched whole, as fixed strings, so a sibling bloc's target never",
		"# counts. grep reads all of safe's output rather than quitting early,",
		"# and the listing ends in || true, so neither SIGPIPE nor pipefail can",
		"# end the script when safe is missing or lists nothing. SAFE_TARGET is",
		"# dropped, because safe refuses to list targets when it names one that",
		"# safe does not know.",
		`BLOC_SAFE_TARGET=""`,
		`if [ -n "${OCFP_BLOC:-}" ]; then`,
		`    OCFP_SAFE_TARGET_NAMES="$(env -u SAFE_TARGET safe targets --json 2>&1 | grep -o '"name"[[:space:]]*:[[:space:]]*"[^"]*"' | sed 's/^"name"[[:space:]]*:[[:space:]]*"\(.*\)"$/\1/' || true)"`,
		`    for OCFP_SAFE_CANDIDATE in "${OCFP_BLOC}-mgmt" "${OCFP_BLOC}-inception"; do`,
		`        if printf '%s\n' "$OCFP_SAFE_TARGET_NAMES" | grep -Fx -- "$OCFP_SAFE_CANDIDATE" >/dev/null; then`,
		`            BLOC_SAFE_TARGET="$OCFP_SAFE_CANDIDATE"`,
		"            break",
		"        fi",
		"    done",
		"fi",
		"# Runs safe against the bloc's own target, and refuses when the bloc has",
		"# none rather than read whichever target is current.",
		"ocfp_bloc_safe() {",
		`    if [ -z "$BLOC_SAFE_TARGET" ]; then`,
		`        printf 'no safe target is registered for bloc %s\n' "${OCFP_BLOC:-}" >&2`,
		"        return 1",
		"    fi",
		`    safe -T "$BLOC_SAFE_TARGET" "$@"`,
		"}",
	}

	for i, line := range lines {
		lines[i] = indent + line
	}

	return lines
}
