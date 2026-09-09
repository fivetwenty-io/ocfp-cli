package provision

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/vault"
)

// pmxContextEnvType is the vault environment segment holding the PVE CPI
// credentials pmx needs to reach the Proxmox API. vault_populate always
// writes the CPI record under mgmt (see PVEVaultProvider.configureCPI),
// regardless of which environments the bloc goes on to deploy: the CPI
// authenticates against the PVE cluster itself, not a bosh environment.
const pmxContextEnvType = vault.MgmtEnvType

// pmxDefaultPort is the Proxmox VE API port used when the CPI vault record
// has no port key (an older vault_populate run, or a hand-edited record).
const pmxDefaultPort = "8006"

// GeneratePMXContextScript generates the script that builds the bastion's
// pmx context from the PVE CPI credentials vault_populate wrote to
// secret/config/{bloc}/mgmt/cpi/pve, so `pmx` can drive the Proxmox API
// without an operator hand-typing `pmx ctx add`.
//
// The caller (Manager.runPMXContext in bastion/phases.go) gates this phase to
// PVE blocs before ever generating the script, but the vault path here is
// built the same way pve_provider.go's configureCPI built it when it wrote
// the record — through vault.PathBuilder.GetEnvironmentPath — so the bloc
// name and environment segment are never hand-spelled in either place.
//
// The generated script:
//   - reads host, node, token_id, and token_secret with `safe get`, failing
//     the phase with a clear error when any of the four is absent;
//   - reads port and verify_ssl the same way, falling back to 8006 and to
//     the bloc config's verify_ssl value when the vault record predates
//     those keys;
//   - splits token_id (form user@realm!tokenname) on '!' into the
//     --username and --token-id pmx expects;
//   - passes token_secret straight from a `safe get` command substitution
//     into `pmx ctx add --secret`, never through a named variable that could
//     end up in a log line;
//   - runs `pmx ctx add <bloc>-cpi ... --select --force`, so a rerun
//     replaces the context instead of failing on "already exists";
//   - tightens the pmx config file to mode 0600 if it is not already; and
//   - verifies the context with `pmx ctx list` and an authenticated
//     `pmx version` call, failing the phase if either does not confirm it.
//
//nolint:funlen // shell script generation with line-by-line append is inherently verbose
func (om *OCFPManager) GeneratePMXContextScript(_ctx context.Context) string {
	cpiPath := vault.NewPathBuilder(om.config, om.config.Name).GetEnvironmentPath(pmxContextEnvType) + "/cpi/pve"

	defaultVerifySSL := "false"
	if om.config.VerifySSL {
		defaultVerifySSL = "true"
	}

	lines := make([]string, 0, scriptBufferOCFP2)
	lines = append(lines,
		"# pmx context setup (PVE bastions only)",
		"",
		fmt.Sprintf("CPI_PATH=%q", cpiPath),
		`PMX_CONTEXT_NAME="${OCFP_BLOC}-cpi"`,
		"",
		`log_info "Configuring pmx context ${PMX_CONTEXT_NAME} from ${CPI_PATH}"`,
		"",
	)

	lines = append(lines, om.pmxRequiredFieldsSnippet()...)
	lines = append(lines, om.pmxOptionalFieldsSnippet(defaultVerifySSL)...)
	lines = append(lines, om.pmxTokenSplitSnippet()...)
	lines = append(lines, om.pmxContextAddSnippet()...)
	lines = append(lines, om.pmxConfigPermissionsSnippet()...)
	lines = append(lines, om.pmxVerifySnippet()...)

	return strings.Join(lines, "\n")
}

// pmxRequiredFieldsSnippet checks that host, node, token_id, and
// token_secret all exist at CPI_PATH before reading any of them, so a bloc
// whose vault_populate never ran (or ran before this phase existed) fails
// with one clear message naming the missing key instead of a pmx flag error
// three steps later. `safe exists` never prints the value, so this check
// leaks nothing even for token_secret.
//
//nolint:funcorder // helper placed after the exported method that uses it
func (om *OCFPManager) pmxRequiredFieldsSnippet() []string {
	return []string{
		`for PMX_FIELD in host node token_id token_secret; do`,
		`    if ! safe exists "${CPI_PATH}:${PMX_FIELD}"; then`,
		`        log_error "pmx_context: ${CPI_PATH}:${PMX_FIELD} not found in vault (run vault_populate first)"`,
		"        exit 1",
		"    fi",
		"done",
		"",
		`PMX_HOST="$(safe get "${CPI_PATH}:host")"`,
		`PMX_NODE="$(safe get "${CPI_PATH}:node")"`,
		`PMX_TOKEN_ID_FULL="$(safe get "${CPI_PATH}:token_id")"`,
		"",
		`if [ -z "$PMX_HOST" ] || [ -z "$PMX_NODE" ] || [ -z "$PMX_TOKEN_ID_FULL" ]; then`,
		`    log_error "pmx_context: host, node, or token_id resolved empty from ${CPI_PATH}"`,
		"    exit 1",
		"fi",
		"",
	}
}

// pmxOptionalFieldsSnippet reads port and verify_ssl, which a vault record
// written before those keys existed may not have. defaultVerifySSL is the
// bloc config's verify_ssl value ("true"/"false"), resolved Go-side because
// it is not a secret and is known at script-generation time.
//
//nolint:funcorder // helper placed after the exported method that uses it
func (om *OCFPManager) pmxOptionalFieldsSnippet(defaultVerifySSL string) []string {
	return []string{
		`PMX_PORT="$(safe get "${CPI_PATH}:port" 2>/dev/null || echo '')"`,
		`if [ -z "$PMX_PORT" ]; then`,
		`    PMX_PORT="` + pmxDefaultPort + `"`,
		"fi",
		"",
		`PMX_VERIFY_SSL="$(safe get "${CPI_PATH}:verify_ssl" 2>/dev/null || echo '')"`,
		`if [ -z "$PMX_VERIFY_SSL" ]; then`,
		`    PMX_VERIFY_SSL="` + defaultVerifySSL + `"`,
		"fi",
		"",
	}
}

// pmxTokenSplitSnippet splits token_id (form user@realm!tokenname) into the
// pmx --username and --token-id values, with `cut -d'!'` exactly as the
// operator's manual recipe does, so a script printed for debugging matches
// the command an operator would type by hand.
//
//nolint:funcorder // helper placed after the exported method that uses it
func (om *OCFPManager) pmxTokenSplitSnippet() []string {
	return []string{
		`PMX_USERNAME="$(printf '%s' "$PMX_TOKEN_ID_FULL" | cut -d'!' -f1)"`,
		`PMX_TOKEN_ID="$(printf '%s' "$PMX_TOKEN_ID_FULL" | cut -d'!' -f2)"`,
		"",
		`if [ -z "$PMX_USERNAME" ] || [ -z "$PMX_TOKEN_ID" ] || [ "$PMX_USERNAME" = "$PMX_TOKEN_ID_FULL" ]; then`,
		`    log_error "pmx_context: token_id at ${CPI_PATH} is not in user@realm!tokenname form"`,
		"    exit 1",
		"fi",
		"",
	}
}

// pmxContextAddSnippet runs `pmx ctx add`. token_secret is interpolated
// straight from a `safe get` command substitution into the --secret
// argument, never assigned to a named variable — so it cannot end up in a
// log line, a `set -x` trace (the bastion preamble never sets -x), or any
// other diagnostic this script emits. --force makes a rerun replace the
// context instead of failing on "already exists"; --select makes it the
// active context so an operator's next `pmx pve ...` call needs no -c flag.
//
//nolint:funcorder // helper placed after the exported method that uses it
func (om *OCFPManager) pmxContextAddSnippet() []string {
	return []string{
		"PMX_CTX_ARGS=(",
		"    ctx add \"$PMX_CONTEXT_NAME\"",
		`    --host "$PMX_HOST"`,
		`    --port "$PMX_PORT"`,
		`    --username "$PMX_USERNAME"`,
		`    --token-id "$PMX_TOKEN_ID"`,
		`    --secret "$(safe get "${CPI_PATH}:token_secret")"`,
		`    --default-node "$PMX_NODE"`,
		"    --select",
		"    --force",
		")",
		"",
		`if [ "$PMX_VERIFY_SSL" != "true" ]; then`,
		`    PMX_CTX_ARGS+=(--insecure)`,
		"fi",
		"",
		`if ! pmx "${PMX_CTX_ARGS[@]}"; then`,
		`    log_error "pmx_context: pmx ctx add ${PMX_CONTEXT_NAME} failed"`,
		"    exit 1",
		"fi",
		"",
	}
}

// pmxConfigPermissionsSnippet tightens the pmx config file to 0600 if it is
// not already. pmx itself writes new config files at 0600 (verified against
// the installed CLI), but a file left behind by `pmx init config`, a manual
// edit, or an older pmx build could be more permissive, and the file holds
// every context's token secret in plaintext.
//
//nolint:funcorder // helper placed after the exported method that uses it
func (om *OCFPManager) pmxConfigPermissionsSnippet() []string {
	return []string{
		`PMX_CONFIG_PATH="${XDG_CONFIG_HOME:-$HOME/.config}/pmx/config.yml"`,
		`if [ -f "$PMX_CONFIG_PATH" ]; then`,
		`    PMX_CONFIG_MODE="$(stat -c '%a' "$PMX_CONFIG_PATH" 2>/dev/null || echo '')"`,
		`    if [ "$PMX_CONFIG_MODE" != "600" ]; then`,
		`        log_warning "pmx config ${PMX_CONFIG_PATH} was mode ${PMX_CONFIG_MODE:-unknown}; tightening to 600"`,
		`        chmod 600 "$PMX_CONFIG_PATH"`,
		"    fi",
		"fi",
		"",
	}
}

// pmxVerifySnippet confirms the context both exists in the config file and
// authenticates against the live PVE API, failing the phase clearly on
// either miss rather than leaving a silently broken context behind.
//
//nolint:funcorder // helper placed after the exported method that uses it
func (om *OCFPManager) pmxVerifySnippet() []string {
	return []string{
		`log_info "Verifying pmx context ${PMX_CONTEXT_NAME}"`,
		"",
		`if ! pmx ctx list -o plain 2>&1 | grep -q "$PMX_CONTEXT_NAME"; then`,
		`    log_error "pmx_context: ${PMX_CONTEXT_NAME} not found in pmx ctx list after add"`,
		"    exit 1",
		"fi",
		"",
		`if ! pmx version --context "$PMX_CONTEXT_NAME" >/dev/null 2>&1; then`,
		`    log_error "pmx_context: pmx version --context ${PMX_CONTEXT_NAME} failed; the PVE API is unreachable or the token is invalid"`,
		"    exit 1",
		"fi",
		"",
		`log_success "pmx context ${PMX_CONTEXT_NAME} configured and verified against ${PMX_HOST}"`,
		"",
	}
}
