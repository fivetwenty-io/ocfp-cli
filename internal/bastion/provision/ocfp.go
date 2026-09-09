package provision

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/deployments"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// OCFPManager handles OCFP-specific provisioning tasks.
type OCFPManager struct {
	config   *config.Config
	provider string
	log      logger.Logger
	modes    *deployments.Resolver
}

// NewOCFPManager creates a new OCFP manager.
func NewOCFPManager(provider string, cfg *config.Config, modes *deployments.Resolver) *OCFPManager {
	if modes == nil {
		modes = deployments.NewResolver(cfg)
	}

	return &OCFPManager{
		config:   cfg,
		provider: provider,
		log:      logger.Get(),
		modes:    modes,
	}
}

// GenerateVaultInceptionScript generates script for vault inception setup.
func (om *OCFPManager) GenerateVaultInceptionScript(_ctx context.Context) string {
	lines := make([]string, 0, 16) //nolint:mnd // rough capacity for script header + locator + execution
	lines = append(lines, "# Vault inception setup")
	lines = append(lines, "")

	// Use OCFP CLI locator to find the installed binary
	lines = append(lines, om.generateOCFPCLILocator()...)
	lines = append(lines, om.generateVaultInceptionExecution()...)

	return strings.Join(lines, "\n")
}

// GenerateOCFPConfigureScript generates the script that sets up the genesis
// deployment repositories on the bastion: kit checkouts, their dev symlinks,
// and `genesis repo-init` for each dev-mode deployment directory.
//
//nolint:funlen // Script generation requires many statements
func (om *OCFPManager) GenerateOCFPConfigureScript(_ctx context.Context) string {
	resolver := om.resolver()
	defaults := defaultDeploymentNamesFor(om.config.SecretsBackendName())
	names := om.mergeDeploymentNames(defaults, resolver.Configured())
	devDeployments, releaseDeployments := om.partitionDeployments(names)

	kitRepos := make(map[string]string, len(devDeployments))
	kitBranches := make(map[string]string, len(devDeployments))

	for _, name := range devDeployments {
		kitRepos[name] = resolver.KitRepo(name)
		kitBranches[name] = resolver.KitBranch(name)
	}

	lines := make([]string, 0, scriptBufferOCFPBase)
	lines = append(lines, "# OCFP deployments setup")
	lines = append(lines, "")

	lines = append(lines, om.generateOCFPCLILocator()...)

	lines = append(lines, fmt.Sprintf("GLOBAL_DEPLOYMENTS_URL=%q", resolver.GlobalURL()))
	lines = append(lines, `DEPLOYMENTS_ROOT="${HOME}/ocfp/deployments"`)
	lines = append(lines, `KITS_ROOT="${HOME}/ocfp/kits"`)
	lines = append(lines, "DEV_DEPLOYMENTS="+formatShellArray(devDeployments))
	lines = append(lines, "RELEASE_DEPLOYMENTS="+formatShellArray(releaseDeployments))
	lines = append(lines, "")

	lines = append(lines, "log_info 'Preparing OCFP deployments'")
	lines = append(lines, `mkdir -p "${DEPLOYMENTS_ROOT}"`)
	lines = append(lines, `mkdir -p "${KITS_ROOT}"`)
	lines = append(lines, "")

	lines = append(lines, `if [ -n "$GLOBAL_DEPLOYMENTS_URL" ]; then`)
	lines = append(lines, `    if [ -d "${DEPLOYMENTS_ROOT}/.git" ]; then`)
	lines = append(lines, `        # Valid git repository exists, update it`)
	lines = append(lines, `        log_info 'Updating deployments repository'`)
	lines = append(lines, `        if git -C "${DEPLOYMENTS_ROOT}" fetch --all --prune && git -C "${DEPLOYMENTS_ROOT}" pull --ff-only; then`)
	lines = append(lines, `            log_success 'Deployments repository updated'`)
	lines = append(lines, "        else")
	lines = append(lines, `            log_warning 'Failed to update deployments repository - please verify connectivity and credentials'`)
	lines = append(lines, "        fi")
	lines = append(lines, `    elif [ -d "${DEPLOYMENTS_ROOT}" ]; then`)
	lines = append(lines, `        # Directory exists but is not a valid git repository`)
	lines = append(lines, `        log_warning 'Deployments directory exists but is not a valid git repository'`)
	lines = append(lines, `        log_info 'Removing invalid deployments directory'`)
	lines = append(lines, `        rm -rf "${DEPLOYMENTS_ROOT}"`)
	lines = append(lines, `        log_info 'Cloning deployments repository'`)
	lines = append(lines, `        if git clone "$GLOBAL_DEPLOYMENTS_URL" "${DEPLOYMENTS_ROOT}"; then`)
	lines = append(lines, `            log_success 'Deployments repository cloned'`)
	lines = append(lines, "        else")
	lines = append(lines, `            log_error 'Failed to clone deployments repository'`)
	lines = append(lines, "        fi")
	lines = append(lines, "    else")
	lines = append(lines, `        # No directory exists, clone fresh`)
	lines = append(lines, `        log_info 'Cloning deployments repository'`)
	lines = append(lines, `        if git clone "$GLOBAL_DEPLOYMENTS_URL" "${DEPLOYMENTS_ROOT}"; then`)
	lines = append(lines, `            log_success 'Deployments repository cloned'`)
	lines = append(lines, "        else")
	lines = append(lines, `            log_error 'Failed to clone deployments repository'`)
	lines = append(lines, "        fi")
	lines = append(lines, "    fi")
	lines = append(lines, "fi")
	lines = append(lines, "")

	lines = append(lines, "# Verify release-mode deployments are present")
	lines = append(lines, `for deployment in "${RELEASE_DEPLOYMENTS[@]}"; do`)
	lines = append(lines, `    DEPLOY_PATH="${DEPLOYMENTS_ROOT}/${deployment}"`)
	lines = append(lines, `    if [ -d "$DEPLOY_PATH" ]; then`)
	lines = append(lines, `        log_success "Release deployment available: ${deployment}"`)
	lines = append(lines, "    else")
	lines = append(lines, `        log_warning "Release deployment directory missing: ${deployment}"`)
	lines = append(lines, "    fi")
	lines = append(lines, "done")
	lines = append(lines, "")

	lines = append(lines, "# Ensure dev-mode deployment directories exist")
	lines = append(lines, `for deployment in "${DEV_DEPLOYMENTS[@]}"; do`)
	lines = append(lines, `    DEPLOY_PATH="${DEPLOYMENTS_ROOT}/${deployment}"`)
	lines = append(lines, `    if [ ! -d "$DEPLOY_PATH" ]; then`)
	lines = append(lines, `        log_info "Creating dev deployment directory: ${deployment}"`)
	lines = append(lines, `        mkdir -p "$DEPLOY_PATH"`)
	lines = append(lines, "    fi")
	lines = append(lines, "done")
	lines = append(lines, "")

	lines = append(lines, "# Clone or refresh the kit checkout for every dev-mode deployment.")
	lines = append(lines, "# The kits come from genesis-community over HTTPS, so this runs whether or")
	lines = append(lines, "# not a deployments repository is configured, and a kit tree that was")
	lines = append(lines, "# rsynced in by hand (no .git) is left exactly as it is.")
	lines = append(lines, "declare -A KIT_REPOS="+formatShellAssoc(kitRepos))
	lines = append(lines, "declare -A KIT_BRANCHES="+formatShellAssoc(kitBranches))
	lines = append(lines, `for deployment in "${DEV_DEPLOYMENTS[@]}"; do`)
	lines = append(lines, `    KIT_REPO="${KIT_REPOS[$deployment]}"`)
	lines = append(lines, `    KIT_BRANCH="${KIT_BRANCHES[$deployment]:-}"`)
	lines = append(lines, `    KIT_DIR="${KITS_ROOT}/${deployment}"`)
	lines = append(lines, `    if [ -d "${KIT_DIR}/.git" ]; then`)
	lines = append(lines, `        log_info "Updating ${deployment} genesis kit"`)
	lines = append(lines, `        if [ -n "$KIT_BRANCH" ] && [ "$(git -C "${KIT_DIR}" rev-parse --abbrev-ref HEAD 2>/dev/null)" != "$KIT_BRANCH" ]; then`)
	lines = append(lines, `            git -C "${KIT_DIR}" fetch --quiet origin "$KIT_BRANCH" && git -C "${KIT_DIR}" checkout --quiet "$KIT_BRANCH" || log_warning "Failed to switch ${deployment} kit to ${KIT_BRANCH}"`)
	lines = append(lines, "        fi")
	lines = append(lines, `        if git -C "${KIT_DIR}" pull --ff-only --quiet; then`)
	lines = append(lines, `            log_success "${deployment} kit updated ($(git -C "${KIT_DIR}" rev-parse --short HEAD))"`)
	lines = append(lines, "        else")
	lines = append(lines, `            log_warning "Failed to update ${deployment} kit"`)
	lines = append(lines, "        fi")
	lines = append(lines, `    elif [ -d "${KIT_DIR}" ] && [ -n "$(ls -A "${KIT_DIR}" 2>/dev/null)" ]; then`)
	lines = append(lines, `        log_info "${deployment} kit at ${KIT_DIR} is not a git checkout; leaving it in place"`)
	lines = append(lines, "    else")
	lines = append(lines, `        log_info "Cloning ${deployment} genesis kit from ${KIT_REPO}${KIT_BRANCH:+ (branch ${KIT_BRANCH})}"`)
	lines = append(lines, `        if git clone --quiet ${KIT_BRANCH:+-b "$KIT_BRANCH"} "$KIT_REPO" "$KIT_DIR"; then`)
	lines = append(lines, `            log_success "${deployment} kit cloned ($(git -C "${KIT_DIR}" rev-parse --short HEAD))"`)
	lines = append(lines, "        else")
	lines = append(lines, `            log_warning "Failed to clone ${deployment} kit from $KIT_REPO"`)
	lines = append(lines, "            continue")
	lines = append(lines, "        fi")
	lines = append(lines, "    fi")
	lines = append(lines, `    mkdir -p "${DEPLOYMENTS_ROOT}/${deployment}"`)
	lines = append(lines, `    ln -sfn "$KIT_DIR" "${DEPLOYMENTS_ROOT}/${deployment}/dev"`)
	lines = append(lines, `    log_info "Linked ${DEPLOYMENTS_ROOT}/${deployment}/dev -> ${KIT_DIR}"`)
	lines = append(lines, "done")
	lines = append(lines, "")

	lines = append(lines, om.generateGenesisRepoInitScript()...)

	return strings.Join(lines, "\n")
}

// defaultDeploymentNamesFor returns the ordered default deployment list for the given
// secrets backend. "openbao" (default) replaces "vault" in the list; "vault" restores it.
func defaultDeploymentNamesFor(secretsBackend string) []string {
	secretsName := "openbao"
	if secretsBackend == "vault" {
		secretsName = "vault"
	}

	return []string{
		"bosh",
		secretsName,
		"concourse",
		"cf",
		"blacksmith",
		"shield",
		"prometheus",
		"doomsday",
		"scheduler",
		"autoscaler",
		"jumpbox",
	}
}

func formatShellArray(values []string) string {
	if len(values) == 0 {
		return "()"
	}

	escaped := make([]string, len(values))
	for i, v := range values {
		escaped[i] = fmt.Sprintf("\"%s\"", v)
	}

	return fmt.Sprintf("(%s)", strings.Join(escaped, " "))
}

// formatShellAssoc renders a bash associative-array literal with keys in a
// stable order. Empty values are kept so every key resolves.
func formatShellAssoc(values map[string]string) string {
	if len(values) == 0 {
		return "()"
	}

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = fmt.Sprintf("[%s]=%q", k, values[k])
	}

	return fmt.Sprintf("(%s)", strings.Join(pairs, " "))
}

//nolint:funcorder,nonamedreturns // Helper method placed after exported methods; named returns for clarity
func (om *OCFPManager) partitionDeployments(names []string) (dev []string, release []string) {
	resolver := om.resolver()

	for _, name := range names {
		if resolver.IsRelease(name) {
			release = append(release, name)
		} else {
			dev = append(dev, name)
		}
	}

	return dev, release
}

//nolint:funcorder // Helper method placed after exported methods
func (om *OCFPManager) mergeDeploymentNames(defaults []string, configured []string) []string {
	seen := make(map[string]struct{}, len(defaults)+len(configured))
	combined := make([]string, 0, len(defaults)+len(configured))

	for _, name := range defaults {
		if name == "" {
			continue
		}

		if _, ok := seen[name]; ok {
			continue
		}

		seen[name] = struct{}{}
		combined = append(combined, name)
	}

	for _, name := range configured {
		if name == "" {
			continue
		}

		if _, ok := seen[name]; ok {
			continue
		}

		seen[name] = struct{}{}
		combined = append(combined, name)
	}

	return combined
}

// generateGenesisRepoInitScript emits the per-deployment `genesis repo-init`
// loop. The deployment root holds one directory per kit (bosh, cf, openbao,
// ...), and every environment of the bloc lives in that kit's directory, so
// the repo name is the kit name and `deployment_type` in .genesis/config
// follows from it.
//
// repo-init only accepts a bare repository name, creates ./<name> under the
// current directory, and with -f/--force deletes an existing target first.
// The deployment directories already hold operator data (env files, ops/,
// and a dev symlink), so the repo is staged in a private temp directory and
// only its .genesis is moved into place. A failed repo-init fails the phase:
// a warning here left a bastion with no .genesis anywhere and a green init.
//
//nolint:funcorder // Helper placed after exported methods
func (om *OCFPManager) generateGenesisRepoInitScript() []string {
	name, email, fallback := om.repoInitGitIdentity()

	lines := []string{
		"# Initialise genesis deployment repos (must exist before env files are written)",
		"# One repo per kit directory. repo-init takes a bare name and creates ./<name>,",
		"# and --force would delete the env files and dev symlink already there, so",
		"# each repo is staged under mktemp and only its .genesis is moved into place.",
		"# --skip-vault defers the secrets provider until the inception step runs.",
		"# Release-mode deployments arrive with their .genesis from the deployments repo.",
		"# repo-init refuses to run without a git identity, so one is exported for the",
		"# genesis invocation alone; the bastion's global git config is left untouched.",
		"",
		"REPO_INIT_GIT_NAME=" + shellSingleQuote(name),
		"REPO_INIT_GIT_EMAIL=" + shellSingleQuote(email),
	}

	if fallback {
		lines = append(lines,
			`log_warning 'Bloc config has no bastion.git.user; genesis will initialise the deployment repos with a placeholder git identity'`,
			`log_warning 'Commits made from the bastion need a real identity: set bastion.git.user.name and bastion.git.user.email, or run git config --global user.name / user.email'`,
		)
	}

	lines = append(lines,
		"",
		`log_info 'Initialising genesis deployment repos'`,
		`for deployment in "${DEV_DEPLOYMENTS[@]}"; do`,
		`    DEPLOY_PATH="${DEPLOYMENTS_ROOT}/${deployment}"`,
		`    KIT_DIR="${KITS_ROOT}/${deployment}"`,
		`    if [ -f "${DEPLOY_PATH}/.genesis/config" ]; then`,
		`        log_info "genesis repo already initialised: ${deployment}"`,
		"        continue",
		"    fi",
		`    if [ ! -d "${KIT_DIR}" ]; then`,
		`        log_info "Kit not staged at ${KIT_DIR}; skipping genesis repo-init for ${deployment}"`,
		"        continue",
		"    fi",
		`    log_info "Running genesis repo-init for ${deployment}"`,
		`    STAGE_DIR=$(mktemp -d)`,
		`    if ! (cd "${STAGE_DIR}" && GIT_AUTHOR_NAME="${REPO_INIT_GIT_NAME}" GIT_AUTHOR_EMAIL="${REPO_INIT_GIT_EMAIL}" genesis repo-init -l "${KIT_DIR}" --skip-vault --no-commit "${deployment}"); then`,
		`        log_error "genesis repo-init failed for ${deployment}"`,
		`        rm -rf "${STAGE_DIR}"`,
		"        exit 1",
		"    fi",
		`    if [ ! -f "${STAGE_DIR}/${deployment}/.genesis/config" ]; then`,
		`        log_error "genesis repo-init wrote no .genesis/config for ${deployment}"`,
		`        rm -rf "${STAGE_DIR}"`,
		"        exit 1",
		"    fi",
		`    mkdir -p "${DEPLOY_PATH}"`,
		`    mv "${STAGE_DIR}/${deployment}/.genesis" "${DEPLOY_PATH}/.genesis"`,
		`    if [ ! -e "${DEPLOY_PATH}/dev" ]; then`,
		`        ln -s "${KIT_DIR}" "${DEPLOY_PATH}/dev"`,
		`        log_info "Linked ${DEPLOY_PATH}/dev -> ${KIT_DIR}"`,
		"    fi",
		`    rm -rf "${STAGE_DIR}"`,
		`    log_success "genesis repo-init completed for ${deployment}"`,
		"done",
		"",
	)

	return lines
}

// repoInitGitIdentity returns the git author identity exported to `genesis
// repo-init`, and whether it is the bloc-derived placeholder. The configured
// bastion.git.user wins when both keys are set; otherwise the placeholder
// names the bloc under the reserved .invalid TLD so it can never route and is
// recognisable as a stand-in if it ever leaks into a commit.
//
//nolint:funcorder,nonamedreturns // Helper placed after exported methods; named returns for clarity
func (om *OCFPManager) repoInitGitIdentity() (name, email string, fallback bool) {
	user := om.config.Bastion.Git.User
	if user.Name != "" && user.Email != "" {
		return user.Name, user.Email, false
	}

	return "ocfp bastion (" + om.config.Name + ")", "ocfp-bastion@" + om.config.Name + ".invalid", true
}

// shellSingleQuote wraps s in single quotes for bash, closing and reopening
// the quotes around any embedded single quote so the value survives verbatim.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

//nolint:funcorder // Helper method placed after exported methods
func (om *OCFPManager) resolver() *deployments.Resolver {
	if om.modes == nil {
		om.modes = deployments.NewResolver(om.config)
	}

	return om.modes
}

// GenerateVaultPopulateScript generates the script for populating vault secrets.
func (om *OCFPManager) GenerateVaultPopulateScript(_ctx context.Context) string {
	lines := make([]string, 0, 32) //nolint:mnd // rough capacity for script sections
	lines = append(lines, "# Vault population")
	lines = append(lines, "")

	lines = append(lines, om.generateOCFPCLILocator()...)
	lines = append(lines, om.generateVaultPopulatePrerequisites()...)
	lines = append(lines, om.generateVaultPreparation()...)
	lines = append(lines, om.generateVaultPopulateExecution()...)

	return strings.Join(lines, "\n")
}

// GenerateGenesisSecretsProvidersScript generates script to configure genesis deployments to use inception vault.
//
//nolint:funlen // shell script generation with line-by-line append is inherently verbose
func (om *OCFPManager) GenerateGenesisSecretsProvidersScript(_ctx context.Context) string {
	lines := make([]string, 0, 71) //nolint:mnd // rough capacity for genesis secrets providers script
	lines = append(lines, "# Configure Genesis secrets providers for deployments")
	lines = append(lines, "")

	lines = append(lines, `DEPLOYMENTS_ROOT="${HOME}/ocfp/deployments"`)
	lines = append(lines, "")

	lines = append(lines, "if [ ! -d \"$DEPLOYMENTS_ROOT\" ]; then")
	lines = append(lines, "    log_warning \"Deployments directory not found: $DEPLOYMENTS_ROOT\"")
	lines = append(lines, "    log_warning 'Skipping genesis secrets-provider configuration'")
	lines = append(lines, "else")
	lines = append(lines, "    log_info 'Configuring Genesis secrets providers for deployments'")
	lines = append(lines, "    ")
	lines = append(lines, "    CONFIGURED_COUNT=0")
	lines = append(lines, "    CLEARED_COUNT=0")
	lines = append(lines, "    SKIPPED_COUNT=0")
	lines = append(lines, "    FAILED_COUNT=0")
	lines = append(lines, "    ")
	lines = append(lines, om.inceptionActiveSnippet()...)
	lines = append(lines, "    for deployment_dir in \"$DEPLOYMENTS_ROOT\"/*; do")
	lines = append(lines, "        if [ ! -d \"$deployment_dir\" ]; then")
	lines = append(lines, "            continue")
	lines = append(lines, "        fi")
	lines = append(lines, "        ")
	lines = append(lines, "        if [ ! -d \"$deployment_dir/.genesis\" ]; then")
	lines = append(lines, "            continue")
	lines = append(lines, "        fi")
	lines = append(lines, "        ")
	lines = append(lines, "        deployment_name=$(basename \"$deployment_dir\")")
	lines = append(lines, "        log_info \"Configuring secrets provider for deployment: $deployment_name\"")
	lines = append(lines, "        ")
	lines = append(lines, "        # Change to deployment directory for genesis command context")
	lines = append(lines, "        if ! cd \"$deployment_dir\"; then")
	lines = append(lines, "            log_warning \"  Failed to enter directory: $deployment_dir\"")
	lines = append(lines, "            SKIPPED_COUNT=$((SKIPPED_COUNT + 1))")
	lines = append(lines, "            continue")
	lines = append(lines, "        fi")
	lines = append(lines, "        ")
	lines = append(lines, om.secretsProviderEmbedSnippet()...)
	lines = append(lines, om.secretsProviderRewriteSnippet()...)
	lines = append(lines, "    done")
	lines = append(lines, "    ")
	lines = append(lines, "    # Return to original directory")
	lines = append(lines, "    cd ~ || true")
	lines = append(lines, "    ")
	lines = append(lines, "    # Summary logging")
	lines = append(lines, "    log_info 'Genesis secrets provider configuration summary:'")
	lines = append(lines, "    log_info \"  Configured: $CONFIGURED_COUNT\"")
	lines = append(lines, "    if [ $CLEARED_COUNT -gt 0 ]; then")
	lines = append(lines, "        log_info \"  Cleared: $CLEARED_COUNT\"")
	lines = append(lines, "    fi")
	lines = append(lines, "    if [ $SKIPPED_COUNT -gt 0 ]; then")
	lines = append(lines, "        log_info \"  Skipped: $SKIPPED_COUNT\"")
	lines = append(lines, "    fi")
	lines = append(lines, "    if [ $FAILED_COUNT -gt 0 ]; then")
	lines = append(lines, "        log_warning \"  Failed: $FAILED_COUNT\"")
	lines = append(lines, "    fi")
	lines = append(lines, "    ")
	lines = append(lines, "    if [ $CLEARED_COUNT -gt 0 ] && [ $CONFIGURED_COUNT -eq 0 ]; then")
	lines = append(lines, "        log_success 'Genesis deployments left on the bloc vault (inception provider cleared)'")
	lines = append(lines, "    elif [ $CONFIGURED_COUNT -gt 0 ]; then")
	lines = append(lines, "        log_success 'Genesis secrets providers configured successfully'")
	lines = append(lines, "    elif [ $FAILED_COUNT -gt 0 ]; then")
	lines = append(lines, "        log_warning 'Some deployments failed to configure - manual intervention may be required'")
	lines = append(lines, "    else")
	lines = append(lines, "        log_info 'No Genesis deployments found to configure'")
	lines = append(lines, "    fi")
	lines = append(lines, "    ")
	lines = append(lines, om.restoreBlocVaultTargetSnippet()...)
	lines = append(lines, "fi")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// secretsProviderEmbedSnippet emits the per-deployment `genesis embed` step.
// Embedding refreshes the deployment's vendored genesis binary so a stale
// embedded version (e.g. 2.8.10) cannot re-exec and silently drop the
// secrets-provider configuration. Non-fatal: a failure is logged and the
// rewrite step still runs.
//
//nolint:funcorder // helper placed after the exported method that uses it
func (om *OCFPManager) secretsProviderEmbedSnippet() []string {
	return []string{
		"        # Heal any stale embedded genesis before touching .genesis/config",
		"        if genesis embed >/dev/null 2>&1; then",
		"            log_info \"  Embedded genesis refreshed for: $deployment_name\"",
		"        else",
		"            log_warning \"  genesis embed failed for $deployment_name (continuing)\"",
		"        fi",
		"        ",
	}
}

// inceptionActiveSnippet decides whether the inception vault is still the
// bloc's source of truth. The inception vault exists only to bootstrap a bloc:
// it is torn down at the end of every init, and it holds nothing once the
// bloc's own vault is up. Genesis 3.2 fails hard when a configured
// secrets_provider is unreachable, so pointing an established bloc's
// deployments at it breaks every manifest render and deploy after an init.
//
// `safe` is targeted at the inception vault for exactly as long as the bloc is
// being bootstrapped, which makes the current target the signal to key off.
// safe reports its target and its target list on stderr, so both streams are
// read; reading stdout alone never matched and left every deployment without
// a provider.
//
//nolint:funcorder // helper placed after the exported method that uses it
func (om *OCFPManager) inceptionActiveSnippet() []string {
	return []string{
		"    # The inception vault is a bootstrap-only store, torn down at the end",
		"    # of every init and empty once the bloc's own vault is up. The bloc",
		"    # vault's safe target is created when that vault is deployed, so its",
		"    # existence is what separates a bloc still being bootstrapped from one",
		"    # already running. Keying off the *current* target is not enough: init",
		"    # points safe at the inception vault while it works.",
		"    # safe prints its target report on stderr, hence the 2>&1 below.",
		"    BLOC_VAULT_TARGET=\"\"",
		"    if [ -n \"${OCFP_BLOC:-}\" ]; then",
		"        BLOC_VAULT_TARGET=\"${OCFP_BLOC}-mgmt\"",
		"    fi",
		"    ",
		"    CURRENT_VAULT_TARGET=\"$(safe target 2>&1 | sed -n 's/^Currently targeting \\(.*\\) at .*$/\\1/p' | head -n 1)\"",
		"    INCEPTION_ACTIVE=no",
		"    INCEPTION_TARGET=\"\"",
		"    if [ -n \"$BLOC_VAULT_TARGET\" ] && safe targets 2>&1 | grep -q \"$BLOC_VAULT_TARGET\"; then",
		"        log_info \"Bloc vault $BLOC_VAULT_TARGET is authoritative; clearing any inception secrets provider\"",
		"    elif printf '%s\\n' \"$CURRENT_VAULT_TARGET\" | grep -q 'inception'; then",
		"        INCEPTION_ACTIVE=yes",
		"        INCEPTION_TARGET=\"$CURRENT_VAULT_TARGET\"",
		"        log_info \"Inception vault $INCEPTION_TARGET is the current target; configuring deployments to use it\"",
		"    else",
		"        log_info 'No bloc vault target found; leaving deployments on the system-targeted vault'",
		"    fi",
		"    ",
	}
}

// restoreBlocVaultTargetSnippet points safe back at the bloc vault once the
// inception work is done. Init targets safe at the inception vault while it
// bootstraps, and nothing else moves it back, so an established bloc would be
// left pointing at a torn-down, empty vault: every later genesis command then
// reports its secrets as missing.
//
//nolint:funcorder // helper placed after the exported method that uses it
func (om *OCFPManager) restoreBlocVaultTargetSnippet() []string {
	return []string{
		"    if [ \"$INCEPTION_ACTIVE\" != yes ] && [ -n \"$BLOC_VAULT_TARGET\" ]; then",
		"        if safe target 2>&1 | grep -q \"$BLOC_VAULT_TARGET\"; then",
		"            log_info \"safe already targets $BLOC_VAULT_TARGET\"",
		"        elif safe target \"$BLOC_VAULT_TARGET\" >/dev/null 2>&1; then",
		"            log_success \"Restored safe target to $BLOC_VAULT_TARGET\"",
		"        else",
		"            log_warning \"Could not restore safe target to $BLOC_VAULT_TARGET\"",
		"        fi",
		"    fi",
		"    ",
	}
}

// secretsProviderClearSnippet removes the deployment's secrets_provider block so
// genesis falls back to the system-targeted vault. `genesis secrets-provider -c`
// is the supported way to do this; a graft merge of a `(( prune ))` fragment
// covers a genesis too old to offer the flag.
//
//nolint:funcorder // helper placed after the exported method that uses it
func (om *OCFPManager) secretsProviderClearSnippet() []string {
	return []string{
		"            if ! grep -q '^secrets_provider:' \"$GENESIS_CONFIG\" 2>/dev/null; then",
		"                log_info \"  Already on the bloc vault: $deployment_name\"",
		"                CLEARED_COUNT=$((CLEARED_COUNT + 1))",
		"                continue",
		"            fi",
		"            ",
		"            if genesis secrets-provider -c >/dev/null 2>&1; then",
		"                log_success \"  Cleared inception secrets provider for: $deployment_name\"",
		"                CLEARED_COUNT=$((CLEARED_COUNT + 1))",
		"            else",
		"                prune_fragment=\"$(mktemp)\"",
		"                printf 'secrets_provider: (( prune ))\\n' > \"$prune_fragment\"",
		"                if graft merge \"$GENESIS_CONFIG\" \"$prune_fragment\" > \"${GENESIS_CONFIG}.tmp\" 2>/dev/null && mv \"${GENESIS_CONFIG}.tmp\" \"$GENESIS_CONFIG\"; then",
		"                    log_success \"  Cleared inception secrets provider (graft) for: $deployment_name\"",
		"                    CLEARED_COUNT=$((CLEARED_COUNT + 1))",
		"                else",
		"                    rm -f \"${GENESIS_CONFIG}.tmp\"",
		"                    log_warning \"  Failed to clear secrets provider for: $deployment_name\"",
		"                    FAILED_COUNT=$((FAILED_COUNT + 1))",
		"                fi",
		"                rm -f \"$prune_fragment\"",
		"            fi",
		"            continue",
	}
}

// secretsProviderRewriteSnippet points the deployment's .genesis/config at the
// bloc inception vault while that vault is the bloc's source of truth.
//
// `genesis secrets-provider <alias>` is the primary path: genesis owns the
// config schema, resolves the alias against the safe targets on the bastion,
// and writes url, insecure, namespace, alias, and strongbox itself. It needs
// no live vault to write the block. The embedded genesis is refreshed just
// before this runs, so the re-exec that once dropped the write to a stale
// binary cannot recur.
//
// If genesis refuses, a graft merge of a small fragment writes the same block
// with the alias safe reports and the port the bastion's own ocfp resolves for
// the inception vault, so the rewritten config cannot point at a port nothing
// is listening on. graft is in the bastion required-tools set; no other YAML
// tool is involved.
//
//nolint:funcorder // helper placed after the exported method that uses it
func (om *OCFPManager) secretsProviderRewriteSnippet() []string {
	// Resolved the same way the bastion's own ocfp resolves it when starting
	// the vault. A literal here is what let the two diverge.
	url := fmt.Sprintf("http://127.0.0.1:%d", config.BlocInceptionVaultPort(om.config))

	lines := []string{
		"        GENESIS_CONFIG=\".genesis/config\"",
		"        if [ ! -f \"$GENESIS_CONFIG\" ]; then",
		"            log_warning \"  No .genesis/config for $deployment_name; skipping\"",
		"            SKIPPED_COUNT=$((SKIPPED_COUNT + 1))",
		"            continue",
		"        fi",
		"        ",
		"        if [ \"$INCEPTION_ACTIVE\" != yes ]; then",
	}

	lines = append(lines, om.secretsProviderClearSnippet()...)

	return append(lines, []string{
		"        fi",
		"        ",
		"        if genesis secrets-provider \"$INCEPTION_TARGET\" >/dev/null 2>&1 && grep -q '^secrets_provider:' \"$GENESIS_CONFIG\" 2>/dev/null; then",
		"            log_success \"  Secrets provider set to $INCEPTION_TARGET for: $deployment_name\"",
		"            CONFIGURED_COUNT=$((CONFIGURED_COUNT + 1))",
		"        else",
		"            log_warning \"  genesis secrets-provider $INCEPTION_TARGET failed for $deployment_name; merging the block with graft\"",
		"            sp_fragment=\"$(mktemp)\"",
		"            printf 'secrets_provider:\\n  alias: %s\\n  insecure: false\\n  namespace: \"\"\\n  strongbox: true\\n  url: %s\\n' \"$INCEPTION_TARGET\" '" + url + "' > \"$sp_fragment\"",
		"            if graft merge \"$GENESIS_CONFIG\" \"$sp_fragment\" > \"${GENESIS_CONFIG}.tmp\" 2>/dev/null && mv \"${GENESIS_CONFIG}.tmp\" \"$GENESIS_CONFIG\"; then",
		"                log_success \"  Secrets provider set to $INCEPTION_TARGET (graft) for: $deployment_name\"",
		"                CONFIGURED_COUNT=$((CONFIGURED_COUNT + 1))",
		"            else",
		"                rm -f \"${GENESIS_CONFIG}.tmp\"",
		"                log_warning \"  Failed to configure secrets provider for: $deployment_name\"",
		"                FAILED_COUNT=$((FAILED_COUNT + 1))",
		"            fi",
		"            rm -f \"$sp_fragment\"",
		"        fi",
	}...)
}

// GenerateOCFPToolVerificationScript generates script to verify required tools after bastion-init.
// vault is always required (the inception vault runs on it regardless of the
// bloc's secrets backend); ruby is required by `bosh create-env` ERB rendering.
func (om *OCFPManager) GenerateOCFPToolVerificationScript(_ctx context.Context) string {
	requiredTools := []string{"safe", "bao", "vault", "bosh", "cf", "credhub", "uaa", "graft", "spruce", "yq", "go", "genesis", "ruby"}

	lines := make([]string, 0, scriptBufferOCFPBase+scriptBufferOCFPPerTool*len(requiredTools))

	lines = append(lines, "# Verify bastion-init prerequisites")
	lines = append(lines, "")

	// tools declared above for capacity

	lines = append(lines, "log_info 'Verifying bastion-init prerequisites'")
	lines = append(lines, "ALL_TOOLS_FOUND=true")
	lines = append(lines, "")

	for _, tool := range requiredTools {
		lines = append(lines, fmt.Sprintf("if command -v %s >/dev/null 2>&1; then", tool))
		lines = append(lines, fmt.Sprintf("    log_info '%s found'", tool))
		lines = append(lines, "else")
		lines = append(lines, fmt.Sprintf("    log_warning '%s not found'", tool))
		lines = append(lines, "    ALL_TOOLS_FOUND=false")
		lines = append(lines, "fi")
	}

	lines = append(lines, "")
	lines = append(lines, "if [ \"$ALL_TOOLS_FOUND\" = \"true\" ]; then")
	lines = append(lines, "    log_success 'All required tools are available'")
	lines = append(lines, "else")
	lines = append(lines, "    log_error 'Some required tools are missing'")
	lines = append(lines, "    log_error 'Please ensure bastion-init provisioning completed successfully'")
	lines = append(lines, "fi")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// GenerateScriptCommandVerificationScript ensures script command is available.
func (om *OCFPManager) GenerateScriptCommandVerificationScript(_ctx context.Context) string {
	lines := make([]string, 0, scriptBufferOCFP1)

	lines = append(lines, "# Verify script command availability")
	lines = append(lines, "")

	lines = append(lines, "if command -v script >/dev/null 2>&1; then")
	lines = append(lines, "    log_success 'script command already available'")
	lines = append(lines, "else")
	lines = append(lines, "    log_info 'Installing script command (bsdutils package)'")
	lines = append(lines, "    sudo apt-get update -qq")
	lines = append(lines, "    sudo apt-get install -y bsdutils")
	lines = append(lines, "    if command -v script >/dev/null 2>&1; then")
	lines = append(lines, "        log_success 'script command installed successfully'")
	lines = append(lines, "    else")
	lines = append(lines, "        log_error 'Failed to install script command'")
	lines = append(lines, "        log_warning 'script command may be required for some operations'")
	lines = append(lines, "    fi")
	lines = append(lines, "fi")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// GenerateHostnameVerificationScript verifies hostname configuration.
func (om *OCFPManager) GenerateHostnameVerificationScript(_ctx context.Context) string {
	lines := make([]string, 0, scriptBufferOCFP2)

	lines = append(lines, "# Hostname verification")
	lines = append(lines, "")

	lines = append(lines, "if [ -n \"$OCFP_BLOC\" ]; then")
	lines = append(lines, "")

	lines = append(lines, "EXPECTED_HOSTNAME=\"${OCFP_BLOC}-bastion\"")
	lines = append(lines, "CURRENT_HOSTNAME=$(hostname)")
	lines = append(lines, "")

	lines = append(lines, "if [ \"$CURRENT_HOSTNAME\" = \"$EXPECTED_HOSTNAME\" ]; then")
	lines = append(lines, "    log_success \"Hostname correctly set to $EXPECTED_HOSTNAME\"")
	lines = append(lines, "else")
	lines = append(lines, "    log_info \"Verifying hostname configuration\"")
	lines = append(lines, "    log_info \"Current hostname: $CURRENT_HOSTNAME\"")
	lines = append(lines, "    log_info \"Expected hostname: $EXPECTED_HOSTNAME\"")
	lines = append(lines, "    ")
	lines = append(lines, "    # Check hostname file")
	lines = append(lines, "    if [ -f \"/etc/hostname\" ]; then")
	lines = append(lines, "        FILE_HOSTNAME=$(cat /etc/hostname)")
	lines = append(lines, "        if [ \"$FILE_HOSTNAME\" = \"$EXPECTED_HOSTNAME\" ]; then")
	lines = append(lines, "            log_success 'Hostname file configured correctly'")
	lines = append(lines, "        else")
	lines = append(lines, "            log_warning \"Hostname file has different value: $FILE_HOSTNAME\"")
	lines = append(lines, "        fi")
	lines = append(lines, "    fi")
	lines = append(lines, "    ")
	lines = append(lines, "    # Check /etc/hosts")
	lines = append(lines, "    if grep -q \"$EXPECTED_HOSTNAME\" /etc/hosts; then")
	lines = append(lines, "        log_success 'Hostname found in /etc/hosts'")
	lines = append(lines, "    else")
	lines = append(lines, "        log_warning 'Hostname not found in /etc/hosts'")
	lines = append(lines, "    fi")
	lines = append(lines, "fi")
	lines = append(lines, "else")
	lines = append(lines, "    log_info 'No OCFP_BLOC provided, skipping hostname verification'")
	lines = append(lines, "fi")
	lines = append(lines, "")

	return strings.Join(lines, "\n")
}

// GenerateEnvironmentLoggingScript generates detailed environment logging.
func (om *OCFPManager) GenerateEnvironmentLoggingScript(_ctx context.Context) string {
	lines := make([]string, 0, scriptBufferOCFP2)

	lines = append(lines, "# Environment information logging")
	lines = append(lines, "")

	lines = append(lines, om.generateSystemInfoLogging()...)
	lines = append(lines, om.generateDiskSpaceLogging()...)
	lines = append(lines, om.generateEnvironmentVariableLogging()...)

	return strings.Join(lines, "\n")
}

func (om *OCFPManager) generateVaultInceptionExecution() []string {
	return []string{
		"# Run vault inception using OCFP CLI binary on bastion",
		"if [ -n \"$OCFP_CLI_PATH\" ]; then",
		"    # Source Linuxbrew environment for vault, safe, and other brew-installed tools",
		"    if [ -x /home/linuxbrew/.linuxbrew/bin/brew ]; then",
		"        eval \"$(/home/linuxbrew/.linuxbrew/bin/brew shellenv)\"",
		"    fi",
		"    export PATH=\"/usr/local/bin:${PATH}\"",
		"    ",
		"    # Ensure TERM/TERMINFO for tmux in non-PTY SSH",
		"    if [ -d /home/linuxbrew/.linuxbrew/share/terminfo ]; then",
		"        export TERMINFO_DIRS=\"${TERMINFO_DIRS:+${TERMINFO_DIRS}:}/home/linuxbrew/.linuxbrew/share/terminfo:/usr/share/terminfo:/lib/terminfo\"",
		"    fi",
		"    if [ -z \"$TERM\" ] || ! infocmp \"$TERM\" >/dev/null 2>&1; then",
		"        for t in xterm-256color screen-256color screen xterm dumb; do",
		"            if infocmp \"$t\" >/dev/null 2>&1; then export TERM=\"$t\"; break; fi",
		"        done",
		"    fi",
		"    ",
		fmt.Sprintf("    VAULT_PORT=%d", config.BlocInceptionVaultPort(om.config)),
		"    VAULT_ADDR=\"http://127.0.0.1:${VAULT_PORT}\"",
		"    if [ -n \"$OCFP_BLOC\" ]; then",
		"        INCEPTION_SESSION=\"${OCFP_BLOC}-inception-vault\"",
		"    else",
		"        INCEPTION_SESSION=\"inception-vault\"",
		"    fi",
		"    ",
		"    # Fast-path: skip if tmux session exists, vault responds, and safe target set",
		"    if tmux has-session -t \"${INCEPTION_SESSION}\" 2>/dev/null \\",
		"       && VAULT_ADDR=\"${VAULT_ADDR}\" vault status >/dev/null 2>&1 \\",
		"       && safe target 2>&1 | grep -q 'inception'; then",
		"        log_success 'Inception vault already running - skipping'",
		"        export VAULT_ADDR",
		"    else",
		"        # Run full inception",
		"        log_info 'Running vault inception via OCFP CLI'",
		"        INCEPTION_ARGS=(vault inception)",
		"        if [ -n \"${OCFP_BLOC:-}\" ]; then",
		"            INCEPTION_ARGS+=(\"--bloc\" \"${OCFP_BLOC}\")",
		"        fi",
		"        PATH=\"/usr/local/bin:${PATH}\" \"${OCFP_CLI_PATH}\" \"${INCEPTION_ARGS[@]}\"",
		"        VAULT_INCEPTION_EXIT=$?",
		"        ",
		"        if [ $VAULT_INCEPTION_EXIT -eq 0 ]; then",
		"            log_success 'Vault inception completed successfully'",
		"        else",
		"            log_error \"Vault inception failed with exit code $VAULT_INCEPTION_EXIT\"",
		"            # Check if it's because vault is already set up",
		"            if safe target 2>&1 | grep -q 'inception\\|production'; then",
		"                log_success 'Vault already configured'",
		"            else",
		"                log_error 'Vault inception failed - vault may need manual setup'",
		"                exit 1",
		"            fi",
		"        fi",
		"    fi",
		"else",
		"    log_error 'OCFP CLI not found at expected locations'",
		"    log_error 'Cannot proceed without vault initialization'",
		"    exit 1",
		"fi",
		"",
	}
}

func (om *OCFPManager) generateVaultPopulatePrerequisites() []string {
	return []string{
		"# Check prerequisites for vault populate",
		"if [ -z \"$OCFP_CLI_PATH\" ]; then",
		"    log_warning 'OCFP CLI not found, skipping vault populate'",
		"elif [ -z \"$OCFP_BLOC\" ] || [ -z \"$OCFP_PROVIDER\" ]; then",
		"    log_warning 'Missing required environment variables for vault populate'",
		"    log_warning \"OCFP_BLOC: ${OCFP_BLOC:-not set}\"",
		"    log_warning \"OCFP_PROVIDER: ${OCFP_PROVIDER:-not set}\"",
		"else",
		"    log_info \"Running vault populate for bloc: $OCFP_BLOC\"",
		"",
	}
}

func (om *OCFPManager) generateOCFPCLILocator() []string {
	return []string{
		"# Locate OCFP CLI",
		"OCFP_LOCATIONS=(",
		"    \"${HOME}/ocfp/cli/bin/ocfp\"",
		"    \"${HOME}/ocfp/cli/ocfp\"",
		"    \"${HOME}/ocfp/ocfp-cli/bin/ocfp\"",
		"    \"/usr/local/bin/ocfp\"",
		")",
		"",
		"OCFP_CLI_PATH=\"\"",
		"for location in \"${OCFP_LOCATIONS[@]}\"; do",
		"    if [ -x \"$location\" ]; then",
		"        OCFP_CLI_PATH=\"$location\"",
		"        log_info \"Found OCFP CLI at: $location\"",
		"        break",
		"    fi",
		"done",
		"",
		"if [ -z \"$OCFP_CLI_PATH\" ]; then",
		"    log_warning 'OCFP CLI not found - some operations may be skipped'",
		"fi",
		"",
	}
}

func (om *OCFPManager) generateVaultPreparation() []string {
	return []string{
		"# Wait for vault to settle",
		"log_info 'Waiting for vault to settle...'",
		"sleep 3",
		"",
		"# Verify vault accessibility",
		"VAULT_CHECK=$(safe target 2>&1 || echo 'not-accessible')",
		"if echo \"$VAULT_CHECK\" | grep -q 'inception\\|production'; then",
		"    log_info 'Vault is accessible and targeted'",
		"else",
		"    log_warning 'Vault may not be properly initialized yet'",
		"    log_warning \"Current vault target: $VAULT_CHECK\"",
		"fi",
		"",
	}
}

func (om *OCFPManager) generateVaultPopulateExecution() []string {
	return []string{
		"    # Execute vault populate",
		`    VAULT_ARGS=("vault" "populate")`,
		"    if [ -n \"${OCFP_BLOC}\" ]; then",
		`        VAULT_ARGS+=("--bloc" "${OCFP_BLOC}")`,
		"    fi",
		"    ",
		"    log_info \"Running: ${OCFP_CLI_PATH} ${VAULT_ARGS[*]}\"",
		`    "${OCFP_CLI_PATH}" "${VAULT_ARGS[@]}"`,
		"    VAULT_EXIT=$?",
		"    ",
		"    if [ $VAULT_EXIT -eq 0 ]; then",
		"        log_success 'Vault populate completed successfully'",
		"        ",
		"        # Verify vault populate results",
		"        log_info 'Verifying vault populate...'",
		"        VERIFY_OUTPUT=$(safe tree \"secret/config/${OCFP_BLOC}\" 2>&1 | head -10 || echo 'verification-failed')",
		"        if echo \"$VERIFY_OUTPUT\" | grep -q 'secret/config'; then",
		"            log_success 'Vault populate verification passed'",
		"            log_info \"Found paths in vault: $(echo \"$VERIFY_OUTPUT\" | head -3)\"",
		"        else",
		"            log_warning 'Could not verify vault populate results'",
		"        fi",
		"    else",
		"        log_error \"Vault populate failed with exit code $VAULT_EXIT\"",
		"        log_warning 'Continuing without vault populate. You may need to run \"ocfp vault populate\" manually later.'",
		"    fi",
		"fi",
		"",
	}
}

func (om *OCFPManager) generateSystemInfoLogging() []string {
	return []string{
		"log_info 'Environment Details:'",
		"log_info \"  Hostname: $(hostname -f 2>/dev/null || hostname)\"",
		"log_info \"  Kernel: $(uname -r)\"",
		"log_info \"  Distribution: $(lsb_release -d 2>/dev/null | cut -f2 || grep PRETTY_NAME /etc/os-release | cut -d= -f2)\"",
		"log_info \"  CPU count: $(nproc)\"",
		"log_info \"  Memory: $(free -h | grep Mem | awk '{print $2}')\"",
		"log_info \"  Current user: $USER (UID: $(id -u))\"",
		"log_info \"  Home directory: $HOME\"",
		"",
	}
}

func (om *OCFPManager) generateDiskSpaceLogging() []string {
	return []string{
		"# Log disk space",
		"log_info 'Disk space:'",
		"df -h / /opt 2>/dev/null | grep '^/' | while read filesystem size used avail percent mount; do",
		"    log_info \"  $mount: $used/$size ($percent)\"",
		"done",
		"",
	}
}

func (om *OCFPManager) generateEnvironmentVariableLogging() []string {
	return []string{
		"# Log OCFP-related environment variables",
		"log_info 'OCFP-related environment variables:'",
		"env | grep -E '^(OCFP|STACKIT|GENESIS|VAULT|SAFE|BOSH|GO)' | sort | while read envvar; do",
		"    log_info \"  $envvar\"",
		"done",
		"",
	}
}
