package provision

import (
	"context"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// TestGeneratePMXContextScript_ContainsExpectedPatterns pins the pieces of
// the generated script that make it match the operator's manual recipe: the
// safe path built from the bloc name, the cut -d'!' split of token_id, the
// secret passed straight from a safe get command substitution, and the
// --force/--select rerun semantics.
func TestGeneratePMXContextScript_ContainsExpectedPatterns(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Name: "ocfp-cf1-lab", Provider: "pve"}
	script := NewOCFPManager("pve", cfg, nil).GeneratePMXContextScript(context.Background())

	for _, want := range []string{
		`CPI_PATH="secret/config/ocfp-cf1-lab/mgmt/cpi/pve"`,
		`PMX_CONTEXT_NAME="${OCFP_BLOC}-cpi"`,
		`safe exists "${CPI_PATH}:${PMX_FIELD}"`,
		`PMX_HOST="$(safe get "${CPI_PATH}:host")"`,
		`PMX_NODE="$(safe get "${CPI_PATH}:node")"`,
		`PMX_TOKEN_ID_FULL="$(safe get "${CPI_PATH}:token_id")"`,
		`cut -d'!' -f1`,
		`cut -d'!' -f2`,
		`--secret "$(safe get "${CPI_PATH}:token_secret")"`,
		"--default-node \"$PMX_NODE\"",
		"--select",
		"--force",
		`pmx ctx list -o plain`,
		`pmx version --context "$PMX_CONTEXT_NAME"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("expected pmx context script to contain %q\ngot:\n%s", want, script)
		}
	}
}

// TestGeneratePMXContextScript_NeverLogsSecret guards the requirement that
// token_secret is never assigned to a named variable (which could end up in
// a log line) and that the script never turns on xtrace, which would print
// every command including the --secret argument to stderr.
func TestGeneratePMXContextScript_NeverLogsSecret(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Name: "ocfp-cf1-lab", Provider: "pve"}
	script := NewOCFPManager("pve", cfg, nil).GeneratePMXContextScript(context.Background())

	for _, reject := range []string{
		"set -x",
		"PMX_TOKEN_SECRET=",
		"PMX_SECRET=",
		"echo \"$PMX",
		"log_info \"$(safe get",
	} {
		if strings.Contains(script, reject) {
			t.Errorf("pmx context script must not contain %q (secret handling regression)\ngot:\n%s", reject, script)
		}
	}

	// The one and only place token_secret's value is read must be the
	// --secret argument itself.
	if got := strings.Count(script, "token_secret"); got != 2 {
		t.Errorf("expected exactly two references to token_secret (the safe exists check and the --secret arg), got %d\ngot:\n%s", got, script)
	}
}

// TestGeneratePMXContextScript_InsecureGatedOnVerifySSL verifies --insecure
// is appended only when the resolved verify_ssl is not "true": the vault
// record wins when present, and the bloc config's VerifySSL is the fallback
// otherwise.
func TestGeneratePMXContextScript_InsecureGatedOnVerifySSL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		verifySSL    bool
		wantFallback string
		wantInsecure bool
	}{
		{name: "verify_ssl false falls back to insecure gate", verifySSL: false, wantFallback: `PMX_VERIFY_SSL="false"`, wantInsecure: true},
		{name: "verify_ssl true falls back to secure gate", verifySSL: true, wantFallback: `PMX_VERIFY_SSL="true"`, wantInsecure: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{Name: "ocfp-cf1-lab", Provider: "pve", VerifySSL: tc.verifySSL}
			script := NewOCFPManager("pve", cfg, nil).GeneratePMXContextScript(context.Background())

			if !strings.Contains(script, tc.wantFallback) {
				t.Errorf("expected script to contain fallback %q\ngot:\n%s", tc.wantFallback, script)
			}

			// --insecure must always be conditionally gated, never unconditional.
			if !strings.Contains(script, `if [ "$PMX_VERIFY_SSL" != "true" ]; then`) {
				t.Errorf("expected --insecure to be gated on PMX_VERIFY_SSL, got:\n%s", script)
			}

			if !strings.Contains(script, "PMX_CTX_ARGS+=(--insecure)") {
				t.Errorf("expected a conditional --insecure append, got:\n%s", script)
			}
		})
	}
}

// TestGeneratePMXContextScript_PortAndVerifySSLFallbacks pins the default
// port and the required-field gate, so a vault record missing port or
// verify_ssl (predating those keys) still produces a working context, while
// one missing host/node/token_id/token_secret fails loudly instead of
// silently building a broken context.
func TestGeneratePMXContextScript_PortAndVerifySSLFallbacks(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Name: "ocfp-cf1-lab", Provider: "pve"}
	script := NewOCFPManager("pve", cfg, nil).GeneratePMXContextScript(context.Background())

	for _, want := range []string{
		`PMX_PORT="$(safe get "${CPI_PATH}:port" 2>/dev/null || echo '')"`,
		`PMX_PORT="8006"`,
		`PMX_VERIFY_SSL="$(safe get "${CPI_PATH}:verify_ssl" 2>/dev/null || echo '')"`,
		"for PMX_FIELD in host node token_id token_secret; do",
		"exit 1",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("expected pmx context script to contain %q\ngot:\n%s", want, script)
		}
	}
}

// TestGeneratePMXContextScript_ConfigPermissions pins that the pmx config
// file's permissions are checked and tightened to 0600 when not already.
func TestGeneratePMXContextScript_ConfigPermissions(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{Name: "ocfp-cf1-lab", Provider: "pve"}
	script := NewOCFPManager("pve", cfg, nil).GeneratePMXContextScript(context.Background())

	for _, want := range []string{
		`PMX_CONFIG_PATH="${XDG_CONFIG_HOME:-$HOME/.config}/pmx/config.yml"`,
		`stat -c '%a' "$PMX_CONFIG_PATH"`,
		`chmod 600 "$PMX_CONFIG_PATH"`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("expected pmx context script to contain %q\ngot:\n%s", want, script)
		}
	}
}
