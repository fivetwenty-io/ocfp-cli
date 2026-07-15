package bastion

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/bastion/provision"
	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
)

// Phase execution errors.
var (
	ErrLocalScriptExecutionNotImplemented = errors.New("local script execution not implemented")
	ErrKeyFetchFailed                     = errors.New("failed to fetch keys")
	ErrInvalidCACertPEM                   = errors.New("resolved bloc CA certificate is not a valid PEM certificate")
	ErrNoCACertAvailable                  = errors.New("no bloc internal CA certificate available")
)

// Additional phase implementations for comprehensive bastion initialization

// runPrerequisiteChecks performs prerequisite validation.
func (m *Manager) runPrerequisiteChecks(ctx context.Context) error {
	m.log.Info("Running prerequisite checks")

	verificationMgr := provision.NewVerificationManager(m.config.Provider, m.config)
	script := verificationMgr.GeneratePreRequisiteCheckScript(ctx)

	return m.executeScript(ctx, script, "prerequisite-checks")
}

// setupOCFPDirectories sets up OCFP-specific directory structure.
func (m *Manager) setupOCFPDirectories(ctx context.Context) error {
	m.log.Info("Setting up OCFP directories")

	dirMgr := provision.NewDirectoryManager(m.config.Provider, m.config)
	script := dirMgr.GenerateOCFPDirectoryScript(ctx)

	return m.executeScript(ctx, script, "ocfp-directories")
}

// brewSkipped reports whether Linuxbrew phases should be skipped for this
// provider. Linuxbrew is the primary tool source on every provider, including
// PVE: the previous PVE skip existed because Linuxbrew's x86_64 bottles need
// SSSE3, which the default kvm64 ("Common KVM processor") VM model lacks — but
// OCFP now provisions PVE VMs with cpu=host (see buildPVEDirectCloudInitConfig),
// exposing the host's SSSE3/AVX so bottles run. CF/Genesis ecosystem tools whose
// brew bottles are macOS-only are still installed via binary_tools.
func (m *Manager) brewSkipped() bool {
	return false
}

// installBrew installs Linuxbrew itself.
func (m *Manager) installBrew(ctx context.Context) error {
	if m.brewSkipped() {
		m.log.Infow("Skipping Linuxbrew install (tools provided via provision script)", "provider", m.config.Provider)

		return nil
	}

	m.log.Info("Installing Linuxbrew")

	brewMgr := provision.NewBrewManager(m.config.Provider, m.config)
	script := brewMgr.GenerateBrewInstallScript(ctx)

	return m.executeScript(ctx, script, "brew-install")
}

// installBrewPackages installs brew packages.
func (m *Manager) installBrewPackages(ctx context.Context) error {
	if m.brewSkipped() {
		m.log.Infow("Skipping brew packages (tools provided via provision script)", "provider", m.config.Provider)

		return nil
	}

	m.log.Info("Installing brew packages")

	brewMgr := provision.NewBrewManager(m.config.Provider, m.config)
	script := brewMgr.GenerateBrewPackageScript(ctx)

	return m.executeScript(ctx, script, "brew-packages")
}

// installPostBrewPackages installs APT packages that have no brew formula,
// after Linuxbrew is available (e.g. libperl-dev, libfuse2).
func (m *Manager) installPostBrewPackages(ctx context.Context) error {
	m.log.Info("Installing post-brew APT packages")

	provCfg := provision.NewConfig(m.config.Provider, m.config, nil)
	packages := map[string]provision.PackageGroup{
		"post_brew": provCfg.GetPostBrewPackages(),
	}

	script := m.buildPackageInstallScript(packages)

	return m.executeScript(ctx, script, "post-brew-apt")
}

// installCPANModules installs CPAN modules.
func (m *Manager) installCPANModules(ctx context.Context) error {
	m.log.Info("Installing CPAN modules")

	cpanMgr := provision.NewCPANManager(m.config.Provider, m.config)
	script := cpanMgr.GenerateCPANInstallScript(ctx)

	return m.executeScript(ctx, script, "cpan-modules")
}

// installCFPlugins installs CloudFoundry plugins.
func (m *Manager) installCFPlugins(ctx context.Context) error {
	m.log.Info("Installing CloudFoundry plugins")

	cfMgr := provision.NewCFPluginManager(m.config.Provider, m.config)
	script := cfMgr.GenerateCFPluginInstallScript(ctx)

	return m.executeScript(ctx, script, "cf-plugins")
}

// createConfigFiles creates configuration files.
func (m *Manager) createConfigFiles(ctx context.Context) error {
	m.log.Info("Creating configuration files")

	configMgr := provision.NewConfigFileManager(m.config.Provider, m.config)
	script := configMgr.GenerateConfigFileScript(ctx)

	return m.executeScript(ctx, script, "config-files")
}

// setupShellEnvironment configures shell environment (.bashrc, aliases).
func (m *Manager) setupShellEnvironment(ctx context.Context) error {
	m.log.Info("Setting up shell environment")

	envMgr := provision.NewEnvironmentManager(m.config.Provider, m.config)
	script := envMgr.GenerateShellEnvironmentScript(ctx)

	return m.executeScript(ctx, script, "shell-environment")
}

// setupSystemEnvironment configures system environment (/etc/environment, /etc/profile.d).
func (m *Manager) setupSystemEnvironment(ctx context.Context) error {
	m.log.Info("Setting up system environment")

	envMgr := provision.NewEnvironmentManager(m.config.Provider, m.config)
	script := envMgr.GenerateSystemEnvironmentScript(ctx)

	return m.executeScript(ctx, script, "system-environment")
}

// setupOCFPCLI sets up OCFP CLI.
func (m *Manager) setupOCFPCLI(ctx context.Context) error {
	m.log.Info("Setting up OCFP CLI")

	// Upload the OCFP binary to the bastion
	err := m.uploadOCFPBinary(ctx)
	if err != nil {
		// In OCFPOnly mode, binary upload is critical
		if m.options.OCFPOnly {
			return fmt.Errorf("OCFP binary upload failed: %w", err)
		}
		// In full init mode, continue anyway - the binary might already be there
		m.log.Warnw("Failed to upload OCFP binary, continuing with setup", "error", err)
	}

	dirMgr := provision.NewDirectoryManager(m.config.Provider, m.config)
	script := dirMgr.GenerateOCFPCLISetupScript(ctx)

	return m.executeScript(ctx, script, "ocfp-cli-setup")
}

// resolveLocalOCFPBinary locates a linux/amd64 ocfp binary on the operator
// machine.  Search order:
//  1. OCFP_BINARY_PATH env var (operator override)
//  2. ./build/ocfp-linux-amd64 (when invoked from the ocfp CLI repo)
//  3. <dir-of-running-ocfp>/../build/ocfp-linux-amd64 (installed sibling)
//  4. ~/w/fivetwenty/studios/ocfp/src/clis/ocfp/build/ocfp-linux-amd64
//     (common developer checkout layout)
//
// Returns the first existing path or an error listing every location tried so
// the operator knows what to fix.
func resolveLocalOCFPBinary() (string, error) {
	if env := os.Getenv("OCFP_BINARY_PATH"); env != "" {
		if _, err := os.Stat(env); err == nil { //nolint:gosec // G703: path is operator-supplied OCFP_BINARY_PATH
			return env, nil
		}
	}

	candidates := []string{"./build/ocfp-linux-amd64"}

	exe, err := os.Executable()
	if err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "..", "build", "ocfp-linux-amd64"))
	}

	home, herr := os.UserHomeDir()
	if herr == nil {
		candidates = append(candidates,
			filepath.Join(home, "w", "fivetwenty", "studios", "ocfp", "src", "clis", "ocfp", "build", "ocfp-linux-amd64"),
		)
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf("ocfp linux/amd64 binary not found; set OCFP_BINARY_PATH or build with 'make build-linux' (searched: %s)", strings.Join(candidates, ", "))
}

// uploadOCFPBinary uploads the OCFP CLI binary to the bastion.
//
//nolint:funlen // sequential upload steps (checksum, transfer, install) must remain together
func (m *Manager) uploadOCFPBinary(ctx context.Context) error {
	// NOTE: Currently uploading from local build until official OCFP releases are published.
	// Once official releases are available via GitHub releases or package repositories,
	// this should be updated to download and install from the official source.
	localBinaryPath, err := resolveLocalOCFPBinary()
	if err != nil {
		return err
	}

	remoteTempPath := "/tmp/ocfp-upload"
	remoteFinalPath := "/usr/local/bin/ocfp"

	m.log.Infow("Setting up OCFP binary", "local", localBinaryPath, "remote", remoteFinalPath)

	// Step 1: Check if remote binary exists and compare checksums
	if m.reporter != nil {
		m.reporter.ReportSubtaskProgress("ocfp_cli_setup", 1, 4, "Checking remote binary") //nolint:mnd
	}

	// Calculate local checksum
	localChecksum, err := calculateFileSHA256(localBinaryPath)
	if err != nil {
		return fmt.Errorf("failed to calculate local binary checksum: %w", err)
	}

	// Check if remote binary exists and get its checksum
	remoteChecksumCmd := fmt.Sprintf("sha256sum '%s' 2>/dev/null | awk '{print $1}' || echo ''", remoteFinalPath)

	remoteResult, err := m.sshClient.ExecuteCommand(ctx, remoteChecksumCmd)
	if err != nil {
		m.log.Debugw("Could not check remote binary checksum", "error", err)
	}

	remoteChecksum := strings.TrimSpace(remoteResult.Stdout)

	// If checksums match, skip upload
	if remoteChecksum != "" && remoteChecksum == localChecksum {
		m.log.Infow("Remote binary already up to date", "checksum", localChecksum)

		if m.reporter != nil {
			m.reporter.ReportSubtaskProgress("ocfp_cli_setup", 4, 4, "Binary already up to date (skipped upload)") //nolint:mnd
		}

		return nil
	}

	m.log.Infow("Binary update needed", "local_checksum", localChecksum, "remote_checksum", remoteChecksum)

	// Step 2: Transfer binary
	if m.reporter != nil {
		m.reporter.ReportSubtaskProgress("ocfp_cli_setup", 2, 4, "Uploading "+localBinaryPath) //nolint:mnd
	}

	// Transfer to temporary location first (user has write permissions here)
	transferOpts := ssh.TransferOptions{
		Recursive:    false,
		Preserve:     false,
		Compress:     false,
		Progress:     nil,
		MaxRetries:   0,
		ChunkSize:    0,
		Verify:       true,
		BackupRemote: false,
	}

	err = m.sshClient.TransferFile(ctx, localBinaryPath, remoteTempPath, transferOpts)
	if err != nil {
		return fmt.Errorf("failed to transfer OCFP binary to temporary location: %w", err)
	}

	// Step 3: Binary uploaded
	if m.reporter != nil {
		m.reporter.ReportSubtaskProgress("ocfp_cli_setup", 3, 4, "Binary uploaded to temporary location") //nolint:mnd
	}

	// Step 4: Install binary with sudo
	if m.reporter != nil {
		m.reporter.ReportSubtaskProgress("ocfp_cli_setup", 4, 4, "Installing to "+remoteFinalPath) //nolint:mnd
	}

	cmd := fmt.Sprintf("sudo mv '%s' '%s' && sudo chmod +x '%s'", remoteTempPath, remoteFinalPath, remoteFinalPath)

	_, err = m.sshClient.ExecuteCommand(ctx, cmd)
	if err != nil {
		// Clean up temp file on failure
		cleanupCmd := fmt.Sprintf("rm -f '%s'", remoteTempPath)
		_, _ = m.sshClient.ExecuteCommand(ctx, cleanupCmd)

		return fmt.Errorf("failed to install OCFP binary to %s: %w", remoteFinalPath, err)
	}

	m.log.Infow("OCFP binary uploaded and made executable", "checksum", localChecksum)

	return nil
}

// helperScripts enumerates operator-facing bash tools shipped to the bastion's
// ~/bin alongside the OCFP CLI. Each entry maps an operator-side source path
// (relative to scripts/) to the basename installed on the bastion.
//
// Add entries here when a new self-contained helper is added to scripts/.
var helperScripts = []struct {
	source string // relative to scripts/
	dest   string // basename installed to ~/bin
}{
	{"blobstores", "blobstores"},
}

// installHelperScripts uploads operator-facing helper tools (currently just
// `blobstores`) from scripts/ to ~/bin on the bastion.  These are bash
// programs that depend only on tools already present after binary_tools +
// brew_packages (safe, aws, jq, curl), so the phase runs after
// ocfp_cli_setup as the natural pairing with the CLI binary install.
//
// Missing source scripts are logged and skipped (not fatal) so an older
// operator checkout that predates a given helper still completes init.
func (m *Manager) installHelperScripts(ctx context.Context) error {
	m.log.Info("Installing operator helper scripts to ~/bin/")

	for _, h := range helperScripts {
		localPath, err := resolveHelperScript(h.source)
		if err != nil {
			m.log.Warnw("Skipping helper script (source not found)",
				"name", h.source, "error", err)

			continue
		}

		remoteTemp := "/tmp/" + h.dest + "-upload"

		opts := ssh.TransferOptions{Verify: true}

		err = m.sshClient.TransferFile(ctx, localPath, remoteTemp, opts)
		if err != nil {
			return fmt.Errorf("transferring helper script %s: %w", h.source, err)
		}

		// ~/bin is created by the directories phase and owned by the SSH user;
		// no sudo needed. mkdir -p is defensive in case install runs out of
		// order or directories phase was skipped.
		installCmd := fmt.Sprintf(
			`bash -c 'mkdir -p "$HOME/bin" && install -m 0755 %q "$HOME/bin/%s" && rm -f %q'`,
			remoteTemp, h.dest, remoteTemp,
		)

		_, err = m.sshClient.ExecuteCommand(ctx, installCmd)
		if err != nil {
			_, _ = m.sshClient.ExecuteCommand(ctx, fmt.Sprintf("rm -f %q", remoteTemp))

			return fmt.Errorf("installing helper script %s: %w", h.dest, err)
		}

		m.log.Infow("Installed helper script", "name", h.dest)
	}

	return nil
}

// resolveHelperScript locates a script under scripts/ on the operator host.
// Search order mirrors resolveLocalOCFPBinary: env override, cwd, sibling of
// the running ocfp binary, and the canonical developer-checkout path.
func resolveHelperScript(name string) (string, error) {
	if env := os.Getenv("OCFP_HELPER_SCRIPTS_DIR"); env != "" {
		p := filepath.Join(env, name)
		if _, err := os.Stat(p); err == nil { //nolint:gosec // G703: path is operator-supplied OCFP_HELPER_SCRIPTS_DIR
			return p, nil
		}
	}

	candidates := []string{filepath.Join("scripts", name)}

	exe, err := os.Executable()
	if err == nil {
		execDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(execDir, "..", "scripts", name),
			filepath.Join(execDir, "scripts", name),
		)
	}

	home, herr := os.UserHomeDir()
	if herr == nil {
		candidates = append(candidates,
			filepath.Join(home, "w", "fivetwenty", "studios", "ocfp", "src", "clis", "ocfp", "scripts", name),
		)
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf("helper script %q not found (searched: %s)", name, strings.Join(candidates, ", "))
}

// setupVaultInception runs vault inception.
func (m *Manager) setupVaultInception(ctx context.Context) error {
	m.log.Info("Setting up vault inception")

	ocfpMgr := provision.NewOCFPManager(m.config.Provider, m.config, m.deploymentModes)
	script := ocfpMgr.GenerateVaultInceptionScript(ctx)

	return m.executeScript(ctx, script, "vault-inception")
}

// runOCFPConfigure runs OCFP configure deployments.
func (m *Manager) runOCFPConfigure(ctx context.Context) error {
	m.log.Info("Running OCFP configure deployments")

	ocfpMgr := provision.NewOCFPManager(m.config.Provider, m.config, m.deploymentModes)
	script := ocfpMgr.GenerateOCFPConfigureScript(ctx)

	return m.executeScript(ctx, script, "ocfp-configure")
}

// runVaultPopulate runs OCFP vault populate.
func (m *Manager) runVaultPopulate(ctx context.Context) error {
	m.log.Info("Running vault populate")

	ocfpMgr := provision.NewOCFPManager(m.config.Provider, m.config, m.deploymentModes)
	script := ocfpMgr.GenerateVaultPopulateScript(ctx)

	return m.executeScript(ctx, script, "vault-populate")
}

// blocCATrustDir is the Debian/Ubuntu location update-ca-certificates scans
// for locally-trusted certificates to fold into /etc/ssl/certs/ca-certificates.crt.
const blocCATrustDir = "/usr/local/share/ca-certificates"

// blocCATrustPath returns the bastion-side path where the bloc's internal CA
// certificate is installed for the system trust store.
func (m *Manager) blocCATrustPath() string {
	return fmt.Sprintf("%s/ocfp-%s-internal-ca.crt", blocCATrustDir, m.config.Name)
}

// installBlocCATrust installs the bloc's internal CA certificate into the
// bastion's system trust store (via update-ca-certificates), so tools that
// consult the OS trust store — curl, safe, vault CLI, the blobstores helper,
// genesis, etc. — can verify TLS connections to the artifacts endpoint (and
// any other internal-ca-issued service) without --insecure/-k flags.
//
// This phase is a deliberate no-op — logged, not an error — whenever the
// bloc has no internal CA to distribute: artifacts disabled, tls.mode is
// self-signed/disabled (those roots are per-instance/ephemeral and not meant
// for a shared OS trust store), or the CA material cannot be resolved from
// local state or vault. A bloc that intends to run internal-ca mode but has
// a genuinely missing CA is a configuration problem already surfaced with
// actionable errors by other phases (vault populate, `artifacts provision`);
// this phase does not duplicate that hard gate, so a bastion re-init never
// fails outright over trust-store convergence alone.
//
// The install itself is idempotent: it hashes the resolved CA PEM and
// compares against the checksum of any file already installed at
// blocCATrustPath, skipping the write and the update-ca-certificates run
// entirely when they already match.
func (m *Manager) installBlocCATrust(ctx context.Context) error {
	if !m.config.Artifacts.Enabled {
		m.log.Infow("Skipping bloc CA trust install: artifacts feature disabled",
			"bloc", m.config.Name)

		return nil
	}

	if m.config.Artifacts.TLS.Mode != config.ArtifactsTLSModeInternalCA {
		m.log.Infow("Skipping bloc CA trust install: artifacts TLS mode is not internal-ca",
			"bloc", m.config.Name, "tls_mode", m.config.Artifacts.TLS.Mode)

		return nil
	}

	caPEM, source, err := m.resolveBlocCACert()
	if err != nil {
		m.log.Warnw("Skipping bloc CA trust install: no internal CA available",
			"bloc", m.config.Name, "error", err)

		return nil
	}

	if err := validateCACertPEM(caPEM); err != nil {
		m.log.Warnw("Skipping bloc CA trust install: resolved CA material is invalid",
			"bloc", m.config.Name, "source", source, "error", err)

		return nil
	}

	m.log.Infow("Installing bloc internal CA into bastion trust store",
		"bloc", m.config.Name, "source", source, "path", m.blocCATrustPath())

	return m.pushCATrustToBastion(ctx, caPEM)
}

// resolveBlocCACert resolves the bloc internal CA certificate PEM operator-
// side (i.e. on the machine running `ocfp bastion init`, before anything is
// pushed over SSH). It tries the local bootstrap state first — the artifacts
// VM resource already carries the CA cert once provisioned, and reading it
// needs no vault connectivity — then falls back to vault directly. Returns
// ErrNoCACertAvailable when neither source has usable material.
func (m *Manager) resolveBlocCACert() (pemText, source string, err error) {
	blocName := m.config.Name
	if blocName == "" {
		return "", "", ErrBlocNameRequired
	}

	if certPEM, ok := m.blocCACertFromState(blocName); ok {
		return certPEM, "state", nil
	}

	certPEM, vaultErr := m.blocCACertFromVault(blocName)
	if vaultErr == nil && certPEM != "" {
		return certPEM, "vault", nil
	}

	if vaultErr != nil {
		return "", "", fmt.Errorf("%w: state has no ca_cert and vault lookup failed: %w", ErrNoCACertAvailable, vaultErr)
	}

	return "", "", fmt.Errorf("%w: state has no ca_cert and vault returned empty material", ErrNoCACertAvailable)
}

// blocCACertFromState reads the operator's local bootstrap state
// (~/.ocfp/<bloc>/state/<bloc>.json) for the artifacts VM resource and
// returns its ca_cert property when the resource is recorded in
// internal-ca mode. Any failure to resolve or load local state is treated
// as "not found here" (ok=false) — resolveBlocCACert falls back to vault.
func (m *Manager) blocCACertFromState(blocName string) (certPEM string, ok bool) {
	stateDir, err := state.GetStateDir(blocName)
	if err != nil {
		m.log.Debugw("Could not resolve local state directory for bloc CA lookup",
			"bloc", blocName, "error", err)

		return "", false
	}

	sm, err := state.NewManager(stateDir)
	if err != nil {
		m.log.Debugw("Could not open local state manager for bloc CA lookup",
			"bloc", blocName, "error", err)

		return "", false
	}

	_, err = sm.Load(blocName)
	if err != nil {
		m.log.Debugw("Could not load local bootstrap state for bloc CA lookup",
			"bloc", blocName, "error", err)

		return "", false
	}

	resource, err := sm.GetResource(artifacts.ResourceType, blocName+"-artifacts")
	if err != nil || resource == nil {
		return "", false
	}

	mode, _ := resource.Properties["tls_mode"].(string)
	if mode != config.ArtifactsTLSModeInternalCA {
		return "", false
	}

	cert, _ := resource.Properties["ca_cert"].(string)
	if strings.TrimSpace(cert) == "" {
		return "", false
	}

	return cert, true
}

// blocCACertFromVault reads secret/ocfp/{bloc}/ca directly via a fresh
// environment-derived vault client (VAULT_ADDR/VAULT_TOKEN or ~/.saferc —
// see vault.NewClientFromEnv). This is the fallback used when local
// bootstrap state has no artifacts resource (e.g. the operator running
// `bastion init` isn't the one who ran bootstrap) or the resource predates
// ca_cert being recorded in state.
func (m *Manager) blocCACertFromVault(blocName string) (string, error) {
	client, err := vault.NewClientFromEnv()
	if err != nil {
		return "", fmt.Errorf("creating vault client from environment: %w", err)
	}

	safe := vault.NewSafe(client)

	mat, err := vault.LoadBlocCA(safe, blocName)
	if err != nil {
		return "", fmt.Errorf("loading bloc CA from vault: %w", err)
	}

	return mat.CertPEM, nil
}

// validateCACertPEM parses caPEM as a single PEM-encoded X.509 certificate,
// rejecting empty input, non-PEM content, non-CERTIFICATE PEM blocks, and
// PEM blocks whose DER payload does not parse as a certificate. This is a
// real parse (not a substring sniff) so corrupted or truncated state/vault
// material is caught before it is pushed to the bastion trust store.
func validateCACertPEM(caPEM string) error {
	if strings.TrimSpace(caPEM) == "" {
		return fmt.Errorf("%w: empty", ErrInvalidCACertPEM)
	}

	block, _ := pem.Decode([]byte(caPEM))
	if block == nil {
		return fmt.Errorf("%w: no PEM block found", ErrInvalidCACertPEM)
	}

	if block.Type != "CERTIFICATE" {
		return fmt.Errorf("%w: PEM block type %q, want CERTIFICATE", ErrInvalidCACertPEM, block.Type)
	}

	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidCACertPEM, err)
	}

	return nil
}

// pushCATrustToBastion installs caPEM at blocCATrustPath and refreshes the
// merged system bundle. It is idempotent: it first checks the SHA-256 of any
// file already at that path and skips the write plus the
// update-ca-certificates run when the content already matches, so repeated
// `ocfp bastion init` runs converge without redundant work or unnecessary
// bundle rebuilds.
//
// The PEM is transferred base64-encoded to sidestep shell quoting entirely —
// certificate PEM contains newlines and the encoded form has no characters
// that need escaping inside the single-quoted remote command.
func (m *Manager) pushCATrustToBastion(ctx context.Context, caPEM string) error {
	remotePath := m.blocCATrustPath()

	localSum := sha256.Sum256([]byte(caPEM))
	localChecksum := hex.EncodeToString(localSum[:])

	checkCmd := fmt.Sprintf("sha256sum '%s' 2>/dev/null | awk '{print $1}' || echo ''", remotePath)

	checkResult, err := m.sshClient.ExecuteCommand(ctx, checkCmd)
	if err != nil {
		m.log.Debugw("Could not check existing bastion CA trust file checksum",
			"path", remotePath, "error", err)
	}

	var remoteChecksum string
	if checkResult != nil {
		remoteChecksum = strings.TrimSpace(checkResult.Stdout)
	}

	if remoteChecksum != "" && remoteChecksum == localChecksum {
		m.log.Infow("Bloc CA already installed in bastion trust store, skipping",
			"path", remotePath, "checksum", localChecksum)

		return nil
	}

	encoded := base64.StdEncoding.EncodeToString([]byte(caPEM))

	installCmd := fmt.Sprintf(
		`bash -c 'echo %s | base64 -d | sudo tee %q > /dev/null && sudo chmod 0644 %q && sudo update-ca-certificates'`,
		encoded, remotePath, remotePath,
	)

	result, err := m.sshClient.ExecuteCommand(ctx, installCmd)
	if err != nil {
		stderr := ""
		if result != nil {
			stderr = extractTail(result.Stderr, 20) //nolint:mnd
		}

		if stderr != "" {
			return fmt.Errorf("installing bloc CA trust at %s: %w\n--- output ---\n%s", remotePath, err, stderr)
		}

		return fmt.Errorf("installing bloc CA trust at %s: %w", remotePath, err)
	}

	m.log.Infow("Bloc CA installed and system trust store refreshed",
		"path", remotePath, "checksum", localChecksum)

	return nil
}

// setupGenesisSecretsProviders configures genesis deployments to use inception vault.
func (m *Manager) setupGenesisSecretsProviders(ctx context.Context) error {
	m.log.Info("Configuring Genesis secrets providers for deployments")

	ocfpMgr := provision.NewOCFPManager(m.config.Provider, m.config, m.deploymentModes)
	script := ocfpMgr.GenerateGenesisSecretsProvidersScript(ctx)

	return m.executeScript(ctx, script, "genesis-secrets-providers")
}

// runHealthCheck performs comprehensive health check.
func (m *Manager) runHealthCheck(ctx context.Context) error {
	m.log.Info("Running health check")

	verificationMgr := provision.NewVerificationManager(m.config.Provider, m.config)
	script := verificationMgr.GenerateHealthCheckScript(ctx)

	return m.executeScript(ctx, script, "health-check")
}

// executeScript is a helper method to execute generated scripts.
func (m *Manager) executeScript(ctx context.Context, script, scriptName string) error {
	if script == "" {
		m.log.Debugw("Skipping empty script", "script", scriptName)

		return nil
	}

	if m.options.DryRun {
		m.log.Infow("DRY RUN: Would execute script", "script", scriptName)
		m.log.Debug("Script content preview",
			"script", scriptName,
			"lines", len(strings.Split(script, "\n")))

		return nil
	}

	// For remote execution via SSH client
	if m.sshClient != nil { //nolint:nestif // sequential SSH command execution with error diagnostics
		// Create script content with proper shebang and functions
		fullScript := m.wrapScriptWithFunctions(script)

		// Execute script inline to avoid file transfer for simple scripts
		cmd := "bash -c " + m.escapeShellString(fullScript)

		result, err := m.sshClient.ExecuteCommand(ctx, cmd)
		if err != nil {
			m.log.Errorw("Script execution failed",
				"script", scriptName,
				"exit_code", result.ExitCode,
				"stdout", result.Stdout,
				"stderr", result.Stderr)

			// Include meaningful script output in the error so users can diagnose failures
			output := extractTail(result.Stderr, 20) //nolint:mnd
			if output == "" {
				output = extractTail(result.Stdout, 20) //nolint:mnd
			}

			if output != "" {
				return fmt.Errorf("script %s failed: %w\n--- script output ---\n%s", scriptName, err, output)
			}

			return fmt.Errorf("script %s failed: %w", scriptName, err)
		}

		m.log.Debugw("Script executed successfully", "script", scriptName, "stdout", result.Stdout)

		return nil
	}

	// For local execution, we would use os/exec
	return ErrLocalScriptExecutionNotImplemented
}

// extractTail returns the last n lines of a string.
// If the string has fewer than n lines, the entire string is returned.
func extractTail(text string, maxLines int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	lines := strings.Split(text, "\n")
	if len(lines) <= maxLines {
		return text
	}

	return strings.Join(lines[len(lines)-maxLines:], "\n")
}

// brewShellEnvSetup sources the Linuxbrew environment so brew-installed binaries are on PATH.
// Used for bare SSH commands (e.g., verification) that don't go through wrapScriptWithFunctions.
const brewShellEnvSetup = `eval "$(/home/linuxbrew/.linuxbrew/bin/brew shellenv)" 2>/dev/null; `

// bashScriptPreamble is the shell boilerplate prepended to bastion provisioning scripts.
const bashScriptPreamble = `#!/bin/bash
set -euo pipefail

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Logging functions
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Initialize script variables
export USER=$(whoami)
export start_time=$(date +%s)
LOG_DIR="${HOME}/.ocfp/logs/provision"
mkdir -p "${LOG_DIR}"
LOG_FILE="${LOG_DIR}/bastion-init-$(date +%Y%m%d-%H%M%S).log"

log_info "Starting script execution at $(date)"
log_info "Log file: ${LOG_FILE}"

# Suppress interactive prompts and debconf warnings
export DEBIAN_FRONTEND=noninteractive
export NEEDRESTART_MODE=a
export NEEDRESTART_SUSPEND=1

# Terminal type for tmux and curses-based tools (not set in non-PTY SSH)
export TERM="${TERM:-screen}"

# Source Linuxbrew environment if available (adds brew bins to PATH)
if [ -x /home/linuxbrew/.linuxbrew/bin/brew ]; then
    eval "$(/home/linuxbrew/.linuxbrew/bin/brew shellenv)"
fi

# Include Linuxbrew terminfo so tmux can find terminal definitions
if [ -d "/home/linuxbrew/.linuxbrew/share/terminfo" ]; then
    export TERMINFO_DIRS="${TERMINFO_DIRS:+${TERMINFO_DIRS}:}/home/linuxbrew/.linuxbrew/share/terminfo:/usr/share/terminfo:/lib/terminfo"
fi

`

// wrapScriptWithFunctions wraps script content with necessary functions.
func (m *Manager) wrapScriptWithFunctions(script string) string {
	envVars := m.getEnvironmentVariables()

	var envExports strings.Builder
	envExports.WriteString("# Export OCFP environment variables\n")

	for key, value := range envVars {
		fmt.Fprintf(&envExports, "export %s='%s'\n", key, value)
	}

	// Phases run via `bash -c` (not a login shell), so /etc/profile.d/ocfp.sh
	// is not sourced. Put Linuxbrew on PATH here when it is installed, so tools
	// installed via brew (vault, yq, openbao, …) are usable by every phase
	// script and not just interactive logins.
	envExports.WriteString("# Linuxbrew on PATH (phases run non-login)\n")
	envExports.WriteString("if [ -x /home/linuxbrew/.linuxbrew/bin/brew ]; then eval \"$(/home/linuxbrew/.linuxbrew/bin/brew shellenv)\"; fi\n")
	envExports.WriteString("\n")

	return bashScriptPreamble + envExports.String() + script
}

// escapeShellString escapes a string for safe shell execution.
func (m *Manager) escapeShellString(script string) string {
	// Simple escaping - in production, this would need more sophisticated escaping
	escaped := strings.ReplaceAll(script, "'", "'\"'\"'")

	return fmt.Sprintf("'%s'", escaped)
}

// calculateFileSHA256 calculates the SHA256 checksum of a file.
func calculateFileSHA256(filePath string) (string, error) {
	file, err := os.Open(filepath.Clean(filePath))
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}

	defer func() { _ = file.Close() }()

	hash := sha256.New()

	_, err = io.Copy(hash, file)
	if err != nil {
		return "", fmt.Errorf("failed to calculate hash: %w", err)
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// configureBastionKeys resolves bastion.keys from config and appends any new
// keys to ~/.ssh/authorized_keys on the bastion host.
func (m *Manager) configureBastionKeys(ctx context.Context) error {
	keys := m.config.Bastion.Keys
	if len(keys) == 0 {
		m.log.Infow("No bastion keys configured, skipping")

		return nil
	}

	m.log.Infow("Configuring bastion SSH authorized keys", "key_count", len(keys))

	keyManager := ssh.NewKeyManager()

	resolved, err := ssh.ResolveKeySpecs(ctx, keys, fetchGitHubKeysHTTP, fetchGitLabKeysHTTP)
	if err != nil {
		return fmt.Errorf("failed to resolve bastion key specs: %w", err)
	}

	if len(resolved) == 0 {
		m.log.Infow("No keys resolved, skipping authorized_keys update")

		return nil
	}

	block := keyManager.FormatAuthorizedKeysBlock(resolved, keys)

	return m.appendNewKeysToBastion(ctx, block, len(resolved))
}

// appendNewKeysToBastion reads the bastion's authorized_keys, deduplicates,
// and appends any keys not already present.
func (m *Manager) appendNewKeysToBastion(ctx context.Context, block string, keyCount int) error {
	result, err := m.sshClient.ExecuteCommand(ctx, "cat ~/.ssh/authorized_keys 2>/dev/null || true")
	if err != nil {
		return fmt.Errorf("failed to read authorized_keys from bastion: %w", err)
	}

	toAppend := deduplicateKeyBlock(block, result.Stdout)
	if strings.TrimSpace(toAppend) == "" {
		m.log.Infow("All bastion keys already present in authorized_keys")

		return nil
	}

	escapedBlock := strings.ReplaceAll(toAppend, "'", "'\"'\"'")
	appendCmd := fmt.Sprintf(
		`bash -c 'mkdir -p ~/.ssh && chmod 700 ~/.ssh && printf "\n%s\n" >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys'`,
		escapedBlock)

	_, err = m.sshClient.ExecuteCommand(ctx, appendCmd)
	if err != nil {
		return fmt.Errorf("failed to append keys to authorized_keys: %w", err)
	}

	m.log.Infow("Bastion authorized keys updated", "keys_added", keyCount)

	return nil
}

// deduplicateKeyBlock returns only lines from block that are not already
// present in existing, preserving comments and blank lines.
func deduplicateKeyBlock(block, existing string) string {
	var newLines []string

	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			newLines = append(newLines, line)

			continue
		}

		if !strings.Contains(existing, line) {
			newLines = append(newLines, line)
		}
	}

	return strings.Join(newLines, "\n")
}

// fetchGitHubKeysHTTP fetches SSH public keys from GitHub for a username.
func fetchGitHubKeysHTTP(ctx context.Context, username string) ([]string, error) {
	return fetchKeysFromURL(ctx, fmt.Sprintf("https://github.com/%s.keys", username), "GitHub")
}

// fetchGitLabKeysHTTP fetches SSH public keys from GitLab for a username.
func fetchGitLabKeysHTTP(ctx context.Context, username string) ([]string, error) {
	return fetchKeysFromURL(ctx, fmt.Sprintf("https://gitlab.com/%s.keys", username), "GitLab")
}

// fetchKeysFromURL performs an HTTP GET and parses newline-delimited SSH keys.
func fetchKeysFromURL(ctx context.Context, url, provider string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build %s key request: %w", provider, err)
	}

	resp, err := http.DefaultClient.Do(req) //nolint:gosec // URL built from trusted config
	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s keys: %w", provider, err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %s returned %s", ErrKeyFetchFailed, provider, resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s keys response: %w", provider, err)
	}

	var keys []string

	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			keys = append(keys, line)
		}
	}

	return keys, nil
}
