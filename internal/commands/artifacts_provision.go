package commands

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// ErrArtifactsStateResourceIncomplete is returned when the artifacts state
// resource is missing endpoint/credential properties needed to re-sync vault
// pins after a re-provision (see updateArtifactsProvisionPins).
var ErrArtifactsStateResourceIncomplete = errors.New("artifacts state resource missing endpoint/credential properties")

const (
	artifactsDefaultS3Port      = 9000
	artifactsDefaultConsolePort = 9001
	artifactsRemoteScript       = "/tmp/provision-artifacts.sh"
	artifactsRemoteLog          = "~/provision-artifacts.log"
)

// artifactsProvision installs and configures RustFS on an already-created
// ocfp-artifacts VM. The VM boots bare (network + SSH only, since PVE 9.x has
// no snippets-capable storage to deliver the RustFS cloud-init); this step
// SSHes in — ProxyJumping through the bloc bastion because the artifacts SDN
// address is not routable from the operator host — and runs the idempotent
// scripts/provision/artifacts installer, then creates the BOSH/CF buckets.
func artifactsProvision(cmd *cobra.Command, acx *artifactsContext, log logger.Logger) error {
	if acx.lookup == nil {
		return fmt.Errorf("%w: %s", ErrArtifactsNotFound, acx.blocName)
	}

	if strings.TrimSpace(acx.lookup.PrivateIP) == "" {
		return fmt.Errorf("artifacts VM %s has no recorded private IP; re-run bootstrap --artifacts", acx.lookup.Name)
	}

	// Preflight the inception vault BEFORE any remote work starts (SSH key
	// resolution, bastion jump setup, script copy) so a missing/sealed/
	// unauthenticated vault fails fast with one actionable message instead of
	// surfacing deep inside resolveArtifactsProvisionTLS after the operator
	// has already waited on SSH setup. Mirrors the bootstrap gate in
	// executeBootstrap (bootstrap.go).
	if acx.lookup.TLSMode == config.ArtifactsTLSModeInternalCA {
		err := ensureArtifactsProvisionVault(acx.blocName, acx.cfg)
		if err != nil {
			return err
		}
	}

	// The bastion is the jump host: reachable from the operator (tailscale /
	// bastion_ip) and able to reach the artifacts SDN address. The artifacts VM
	// authorizes the same bloc keypair as the bastion, so one key serves both
	// hops. --no-proxy-jump bypasses the jump when the operator is already on
	// the SDN (e.g. running this command from the bastion itself), because
	// OpenSSH's implicit ProxyJump inner ssh does not inherit the outer -o
	// options and falls back to default host-key checking, which fails when the
	// jump target's host key is unknown on the local machine.
	noProxyJump, _ := cmd.Flags().GetBool("no-proxy-jump")

	var (
		sshUser  = viper.GetString("ssh.user")
		sshKey   string
		proxyOpt string
	)

	if noProxyJump {
		sshKey = strings.TrimSpace(viper.GetString("ssh.key"))
		if sshKey == "" {
			if k, kerr := findSSHKey(acx.blocName, acx.cfg); kerr == nil {
				sshKey = k
			}
		}
	} else {
		bastionCtx, err := GetBastionContext(cmd, log)
		if err != nil {
			return fmt.Errorf("resolve bastion jump host: %w", err)
		}

		sshUser = bastionCtx.User
		sshKey = bastionCtx.SSHKeyOption
		proxyOpt = fmt.Sprintf("-o ProxyJump=%s@%s", bastionCtx.User, bastionCtx.IP)
	}

	cert, key, caPEM, fingerprint, err := resolveArtifactsProvisionTLS(acx.cfg, acx.lookup, acx.blocName)
	if err != nil {
		return err
	}

	env := buildArtifactsProvisionEnv(acx.cfg, acx.lookup, cert, key, caPEM)
	env["OCFP_BLOC"] = acx.blocName

	scriptPath, err := FindProvisionScript("artifacts")
	if err != nil {
		return fmt.Errorf("cannot find artifacts provision script: %w", err)
	}

	target := acx.lookup.PrivateIP

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	if dryRun {
		jumpDesc := "direct (no ProxyJump)"
		if proxyOpt != "" {
			jumpDesc = proxyOpt
		}

		log.Infof("[dry-run] would scp %s to %s@%s:%s via %s and run it as root",
			scriptPath, sshUser, target, artifactsRemoteScript, jumpDesc)

		return nil
	}

	err = runArtifactsRemoteScript(sshUser, target, sshKey, proxyOpt, scriptPath, renderEnvAssignments(env), log)
	if err != nil {
		return err
	}

	// Re-provision succeeded remotely: re-sync the pinned state/vault values
	// (fingerprint always; ca_cert too for self-signed, where the leaf IS the
	// trust anchor) so a repeated `provision` never leaves stale pins behind.
	// Best-effort/log-only: the remote work already succeeded, and the next
	// provision run converges any partial state/vault write here.
	if cert != "" {
		pinErr := updateArtifactsProvisionPins(acx, cert, fingerprint)
		if pinErr != nil {
			log.Warnf("artifacts: updating state/vault pins after provision: %v", pinErr)
		}
	}

	return nil
}

// ensureArtifactsProvisionVault preflights vault access for internal-ca TLS
// mode the same way bootstrap does (executeBootstrap, bootstrap.go): try a
// plain env-driven vault client first, and only start the inception vault
// (idempotent) when that fails. Runs before any SSH/remote provisioning work
// starts so a missing/sealed/unauthenticated vault fails fast here instead of
// surfacing deep inside resolveArtifactsProvisionTLS.
func ensureArtifactsProvisionVault(blocName string, cfg *config.Config) error {
	_, err := vault.NewManagerFromEnv(cfg, blocName)
	if err == nil {
		return nil
	}

	err = ensureInceptionVault(blocName, viper.GetBool("test"))
	if err != nil {
		return artifacts.InternalCAVaultError(blocName, err)
	}

	_, err = vault.NewManagerFromEnv(cfg, blocName)
	if err != nil {
		return artifacts.InternalCAVaultError(blocName, err)
	}

	return nil
}

// resolveArtifactsProvisionTLS produces the cert + key PEMs (and the new
// cert's fingerprint) the RustFS service needs, matching the bloc's
// configured tls.mode. internal-ca re-issues a leaf from the bloc CA in vault
// (the leaf key is never persisted at create time); self-signed regenerates;
// disabled returns empty strings. The SAN set always includes the loopback
// addresses (127.0.0.1, ::1) in addition to the VM's private IP, so on-VM
// clients (the provisioning script itself, local health checks) can verify
// without falling back to skip-verify.
//
// caPEM is the trust anchor to deliver to the VM's own OS trust store
// (RUSTFS_TLS_CA, see buildArtifactsProvisionEnv), distinct from certPEM (the
// serving leaf): for internal-ca it is the bloc CA cert; for self-signed the
// leaf IS its own trust anchor, so caPEM equals certPEM; disabled has
// neither.
func resolveArtifactsProvisionTLS(cfg *config.Config, lr *artifacts.LookupResult, blocName string) (certPEM, keyPEM, caPEM, fingerprint string, err error) {
	vmName := blocName + "-artifacts"

	commonName := cfg.Artifacts.TLS.CommonName
	if commonName == "" {
		commonName = vmName
	}

	sans := []string{commonName, vmName}

	var vmIP net.IP
	if ip := net.ParseIP(lr.PrivateIP); ip != nil {
		vmIP = ip
	}

	ips := artifacts.ArtifactsLeafSANIPs(vmIP)

	switch lr.TLSMode {
	case config.ArtifactsTLSModeDisabled, "":
		return "", "", "", "", nil
	case config.ArtifactsTLSModeSelfSigned:
		mat, genErr := artifacts.GenerateSelfSignedTLS(commonName, sans, ips)
		if genErr != nil {
			return "", "", "", "", fmt.Errorf("artifacts: generate self-signed TLS: %w", genErr)
		}

		return mat.CertPEM, mat.KeyPEM, mat.CertPEM, mat.Fingerprint, nil
	case config.ArtifactsTLSModeInternalCA:
		mgr, mgrErr := vault.NewManagerFromEnv(cfg, blocName)
		if mgrErr != nil {
			return "", "", "", "", artifacts.InternalCAVaultError(blocName, mgrErr)
		}
		defer func() { _ = mgr.Close() }()

		ca, caErr := vault.LoadOrGenerateBlocCA(mgr.GetSafe(), blocName)
		if caErr != nil {
			return "", "", "", "", fmt.Errorf("artifacts: load bloc CA: %w", caErr)
		}

		leaf, leafErr := artifacts.IssueLeafCert(ca, commonName, sans, ips)
		if leafErr != nil {
			return "", "", "", "", fmt.Errorf("artifacts: issue leaf cert: %w", leafErr)
		}

		return leaf.CertPEM, leaf.KeyPEM, ca.CertPEM, leaf.Fingerprint, nil
	default:
		return "", "", "", "", fmt.Errorf("artifacts: unsupported TLS mode %q", lr.TLSMode)
	}
}

// updateArtifactsProvisionPins re-syncs state (and vault, when reachable)
// with the freshly issued leaf's fingerprint and — for self-signed mode,
// where the leaf itself is the trust anchor — the new ca_cert pin.
// internal-ca mode's pinned ca_cert stays anchored to the bloc CA (unaffected
// by a leaf reissue); only its freshness metadata (fingerprint) is updated.
func updateArtifactsProvisionPins(acx *artifactsContext, certPEM, fingerprint string) error {
	vmName := acx.blocName + "-artifacts"

	res, err := acx.state.GetResource(artifacts.ResourceType, vmName)
	if err != nil {
		return fmt.Errorf("load artifacts state resource: %w", err)
	}

	if res.Properties == nil {
		res.Properties = map[string]interface{}{}
	}

	if fingerprint != "" {
		// tls_fingerprint_sha256 is operator/status metadata only (see the
		// vault.ArtifactsWriter doc comment); never used to make a trust
		// decision — TLS clients verify against ca_cert, not this value.
		res.Properties["tls_fingerprint_sha256"] = fingerprint
	}

	if certPEM != "" {
		if leafCert, perr := parseCACertPEM(certPEM); perr == nil {
			res.Properties["tls_leaf_not_after"] = leafCert.NotAfter.UTC().Format(time.RFC3339)
		} else {
			logger.Warnf("artifacts: parsing re-issued leaf cert for expiry recording: %v", perr)
		}
	}

	if acx.lookup.TLSMode == config.ArtifactsTLSModeSelfSigned && certPEM != "" {
		res.Properties["ca_cert"] = certPEM
	}

	err = acx.state.UpdateResource(res)
	if err != nil {
		return fmt.Errorf("update artifacts state resource: %w", err)
	}

	if fingerprint != "" {
		_ = acx.state.SetOutput("artifacts_tls_fingerprint", fingerprint)
	}

	err = acx.state.Save()
	if err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	return syncArtifactsProvisionVaultMeta(acx, res)
}

// syncArtifactsProvisionVaultMeta re-runs the vault write when vault is
// reachable, so the blobstore config paths and operational metadata
// (tls_fingerprint_sha256, tls_leaf_not_after) pick up the refreshed pin
// without a separate `ocfp vault populate`. Vault unreachable during
// provision is common (the operator did not export VAULT_ADDR/TOKEN for this
// invocation) and must not fail an otherwise-successful provision — the
// caller already treats this as warning-only.
func syncArtifactsProvisionVaultMeta(acx *artifactsContext, res *state.Resource) error {
	mgr, err := vault.NewManagerFromEnv(acx.cfg, acx.blocName)
	if err != nil {
		return nil //nolint:nilerr // vault unavailable during provision is expected/non-fatal; caller logs a warning either way
	}
	defer func() { _ = mgr.Close() }()

	ep, creds, ok := artifactsProvisionEndpointCreds(res, acx.cfg)
	if !ok {
		return ErrArtifactsStateResourceIncomplete
	}

	fp, fpOK := res.Properties["tls_fingerprint_sha256"].(string)
	notAfter, notAfterOK := res.Properties["tls_leaf_not_after"].(string)

	var tlsMat *artifacts.TLSMaterial
	if (fpOK && fp != "") || (notAfterOK && notAfter != "") {
		tlsMat = &artifacts.TLSMaterial{Fingerprint: fp, NotAfter: notAfter}
	}

	writer := vault.NewArtifactsWriter(acx.cfg, mgr.GetSafe(), acx.blocName)

	return writer.WriteArtifacts(acx.parent, acx.blocName, ep, creds, tlsMat)
}

// artifactsProvisionEndpointCreds rebuilds the Endpoint + Credentials needed
// for the post-provision vault re-sync from the (just-updated) state
// resource's properties. Port/region/path-style come from config rather than
// the properties map since numeric values round-trip through JSON as
// float64, and config already holds the authoritative port.
func artifactsProvisionEndpointCreds(res *state.Resource, cfg *config.Config) (artifacts.Endpoint, artifacts.Credentials, bool) {
	get := func(key string) string {
		v, _ := res.Properties[key].(string)

		return v
	}

	endpointURL := get("endpoint")
	host := get("private_ip")
	accessKey := get("access_key")
	secretKey := get("secret_key")

	if endpointURL == "" || host == "" || accessKey == "" || secretKey == "" {
		return artifacts.Endpoint{}, artifacts.Credentials{}, false
	}

	port := cfg.Artifacts.Rustfs.S3Port
	if port == 0 {
		port = artifactsDefaultS3Port
	}

	ep := artifacts.Endpoint{
		URL:       endpointURL,
		Host:      host,
		Port:      port,
		Region:    config.BlobstoreDefaultRegion,
		PathStyle: true,
		CACert:    get("ca_cert"),
	}

	creds := artifacts.Credentials{AccessKey: accessKey, SecretKey: secretKey}

	return ep, creds, true
}

// buildArtifactsProvisionEnv assembles the environment the remote installer
// reads. Credentials + dataset come from the lookup (persisted at create);
// ports, mountpoint, and download URL from config. TLS cert/key are only
// included when TLS is enabled. ca is the trust anchor (bloc CA for
// internal-ca, the leaf itself for self-signed — see
// resolveArtifactsProvisionTLS) delivered as RUSTFS_TLS_CA so the installer
// can install it into the VM's own OS trust store; empty when TLS is
// disabled, in which case the installer falls back to --no-verify-ssl.
func buildArtifactsProvisionEnv(cfg *config.Config, lr *artifacts.LookupResult, cert, key, ca string) map[string]string {
	s3Port := cfg.Artifacts.Rustfs.S3Port
	if s3Port == 0 {
		s3Port = artifactsDefaultS3Port
	}

	consolePort := cfg.Artifacts.Rustfs.ConsolePort
	if consolePort == 0 {
		consolePort = artifactsDefaultConsolePort
	}

	mountpoint := cfg.Artifacts.Data.Mountpoint
	if mountpoint == "" {
		mountpoint = "/data"
	}

	dataset := lr.ZFSDataset
	if dataset == "" {
		dataset = cfg.Artifacts.ResolvedDataset(cfg.Name)
	}

	tlsEnabled := cert != "" && key != ""

	scheme := "http"
	if tlsEnabled {
		scheme = "https"
	}

	env := map[string]string{
		"RUSTFS_ACCESS_KEY":   lr.AccessKey,
		"RUSTFS_SECRET_KEY":   lr.SecretKey,
		"RUSTFS_S3_PORT":      strconv.Itoa(s3Port),
		"RUSTFS_CONSOLE_PORT": strconv.Itoa(consolePort),
		"RUSTFS_VOLUMES":      mountpoint,

		// The installer only consults the dataset when the filesystem is zfs.
		"ARTIFACTS_DATA_FILESYSTEM": cfg.Artifacts.ResolvedFilesystem(),
		"RUSTFS_ZFS_DATASET":        dataset,
		"RUSTFS_DOWNLOAD_URL":       cfg.Artifacts.ResolvedDownloadURL(),
		"RUSTFS_TLS_ENABLED":        strconv.FormatBool(tlsEnabled),
		"ARTIFACTS_BUCKETS":         strings.Join(artifactsProvisionBuckets(cfg.Name), " "),
		"ARTIFACTS_ENDPOINT":        fmt.Sprintf("%s://127.0.0.1:%d", scheme, s3Port),
	}

	if tlsEnabled {
		env["RUSTFS_TLS_CERT"] = cert
		env["RUSTFS_TLS_KEY"] = key
	}

	if ca != "" {
		env["RUSTFS_TLS_CA"] = ca
	}

	return env
}

// artifactsProvisionBuckets enumerates the BOSH + CF buckets to create on the
// artifacts endpoint. Delegates to the canonical list shared with bootstrap's
// artifactsBucketList (internal/artifacts.CanonicalBucketNames) so the two
// provisioning paths can never drift apart on the bucket roster again.
func artifactsProvisionBuckets(blocName string) []string {
	return artifacts.CanonicalBucketNames(blocName)
}

// renderEnvAssignments turns the env map into sorted, single-quote-safe shell
// assignment lines. The remote side sources this file under `set -a` so every
// value is exported into the installer's environment.
func renderEnvAssignments(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		escaped := strings.ReplaceAll(env[k], "'", `'\''`)
		lines = append(lines, fmt.Sprintf("%s='%s'", k, escaped))
	}

	return strings.Join(lines, "\n")
}

// runArtifactsRemoteScript copies the installer to the artifacts VM (through
// the bastion ProxyJump) and runs it as root. The env block is base64-encoded
// and written to a 0600 temp file passed as the script's first argument; the
// script sources it under `set -a` so root sees every value. PIPESTATUS
// preserves the installer's exit code through the `tee`.
func runArtifactsRemoteScript(user, host, keyOpt, proxyOpt, scriptPath, envString string, log logger.Logger) error {
	dest := fmt.Sprintf("%s@%s:%s", user, host, artifactsRemoteScript)

	scpCmd := buildSCPCommand(scriptPath, dest, keyOpt, false, proxyOpt)

	log.Infof("Copying artifacts provision script to %s", host)

	err := executeSCP(context.Background(), scpCmd)
	if err != nil {
		return err
	}

	envB64 := base64.StdEncoding.EncodeToString([]byte(envString))

	remote := fmt.Sprintf(
		// Non-login shell on purpose: under `set -e` a login shell runs
		// ~/.bash_logout on exit, where Ubuntu's clear_console fails without a
		// tty and overrides the script's exit 0 with 1.
		"bash -c 'set -euo pipefail; _e=$(mktemp); chmod 600 \"$_e\"; echo %s | base64 -d > \"$_e\"; sudo bash %s \"$_e\" 2>&1 | tee %s; rc=${PIPESTATUS[0]}; rm -f \"$_e\"; exit $rc'",
		envB64, artifactsRemoteScript, artifactsRemoteLog,
	)

	sshCmd := buildSSHCommand(host, user, keyOpt, proxyOpt, []string{}, []string{})
	sshCmd = append(sshCmd, remote)

	log.Infof("Running artifacts provision on %s (RustFS install + bucket creation)", host)

	err = executeSSH(context.Background(), sshCmd)
	if err != nil {
		return err
	}

	// Best-effort cleanup of the remote script.
	cleanup := buildSSHCommand(host, user, keyOpt, proxyOpt, []string{}, []string{})
	cleanup = append(cleanup, "rm -f "+artifactsRemoteScript)
	_ = executeSSH(context.Background(), cleanup)

	return nil
}
