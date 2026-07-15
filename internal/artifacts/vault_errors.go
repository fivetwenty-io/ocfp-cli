package artifacts

import (
	"fmt"
	"os"
	"strings"
)

// DescribeVaultAddrAttempt reports, for diagnostic error text, the vault
// address a plain env-driven vault client (vault.NewManagerFromEnv /
// vault.NewClientFromEnv) would attempt to reach: VAULT_ADDR when set,
// otherwise the documented fallback chain (~/.saferc target, then
// https://127.0.0.1:8200). This does not replicate the ~/.saferc parsing
// itself — it only describes the fallback so operators know what to check
// without duplicating that (unexported) logic across packages.
func DescribeVaultAddrAttempt() string {
	if addr := strings.TrimSpace(os.Getenv("VAULT_ADDR")); addr != "" {
		return addr
	}

	return "unset (falls back to ~/.saferc target, then https://127.0.0.1:8200)"
}

// InternalCAVaultError builds an actionable error for the case where
// tls.mode=internal-ca needs vault access (to read/mint the bloc CA at
// secret/ocfp/{bloc}/ca) but vault is unreachable or unauthenticated. The
// message names, in order: (1) the fix to start the inception vault, (2) the
// env/`safe target` fallback already attempted, and (3) the self-signed
// escape hatch — plus the bloc name and the vault address that was tried, so
// operators are not left guessing which vault failed or why.
func InternalCAVaultError(blocName string, cause error) error {
	return fmt.Errorf(
		"artifacts: bloc %q requires vault access for internal-ca TLS (attempted vault addr: %s): %w\n"+
			"fix: run `ocfp vault inception --bloc %s` to start the inception vault, "+
			"or export VAULT_ADDR/VAULT_TOKEN (or run `safe target <name>`) to point at a running one, "+
			"or set artifacts.tls.mode: self-signed as a last resort",
		blocName, DescribeVaultAddrAttempt(), cause, blocName)
}
