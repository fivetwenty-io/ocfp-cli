package commands

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

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

	cert, key, err := resolveArtifactsProvisionTLS(acx.cfg, acx.lookup, acx.blocName)
	if err != nil {
		return err
	}

	env := buildArtifactsProvisionEnv(acx.cfg, acx.lookup, cert, key)
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

	return runArtifactsRemoteScript(sshUser, target, sshKey, proxyOpt, scriptPath, renderEnvAssignments(env), log)
}

// resolveArtifactsProvisionTLS produces the cert + key PEMs the RustFS service
// needs, matching the bloc's configured tls.mode. internal-ca re-issues a leaf
// from the bloc CA in vault (the leaf key is never persisted at create time);
// self-signed regenerates; disabled returns empty strings.
func resolveArtifactsProvisionTLS(cfg *config.Config, lr *artifacts.LookupResult, blocName string) (string, string, error) {
	vmName := blocName + "-artifacts"

	commonName := cfg.Artifacts.TLS.CommonName
	if commonName == "" {
		commonName = vmName
	}

	sans := []string{commonName, vmName}

	var ips []net.IP
	if ip := net.ParseIP(lr.PrivateIP); ip != nil {
		ips = append(ips, ip)
	}

	switch lr.TLSMode {
	case config.ArtifactsTLSModeDisabled, "":
		return "", "", nil
	case config.ArtifactsTLSModeSelfSigned:
		mat, genErr := artifacts.GenerateSelfSignedTLS(commonName, sans, ips)
		if genErr != nil {
			return "", "", fmt.Errorf("artifacts: generate self-signed TLS: %w", genErr)
		}

		return mat.CertPEM, mat.KeyPEM, nil
	case config.ArtifactsTLSModeInternalCA:
		mgr, mgrErr := vault.NewManagerFromEnv(cfg, blocName)
		if mgrErr != nil {
			return "", "", fmt.Errorf("artifacts: internal-ca TLS requires vault access; set OCFP_VAULT_ADDR/TOKEN or switch tls.mode: %w", mgrErr)
		}

		ca, caErr := vault.LoadOrGenerateBlocCA(mgr.GetSafe(), blocName)
		if caErr != nil {
			return "", "", fmt.Errorf("artifacts: load bloc CA: %w", caErr)
		}

		leaf, leafErr := artifacts.IssueLeafCert(ca, commonName, sans, ips)
		if leafErr != nil {
			return "", "", fmt.Errorf("artifacts: issue leaf cert: %w", leafErr)
		}

		return leaf.CertPEM, leaf.KeyPEM, nil
	default:
		return "", "", fmt.Errorf("artifacts: unsupported TLS mode %q", lr.TLSMode)
	}
}

// buildArtifactsProvisionEnv assembles the environment the remote installer
// reads. Credentials + dataset come from the lookup (persisted at create);
// ports, mountpoint, and download URL from config. TLS cert/key are only
// included when TLS is enabled.
func buildArtifactsProvisionEnv(cfg *config.Config, lr *artifacts.LookupResult, cert, key string) map[string]string {
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

	return env
}

// artifactsProvisionBuckets enumerates the BOSH + CF buckets to create on the
// artifacts endpoint. Names follow the {bloc}-{env}-{type} convention shared
// with bootstrap's artifactsBucketList.
func artifactsProvisionBuckets(blocName string) []string {
	return []string{
		blocName + "-mgmt-bosh",
		blocName + "-ocf-cf-droplets",
		blocName + "-ocf-cf-packages",
		blocName + "-ocf-cf-buildpacks",
		blocName + "-ocf-cf-resource-pool",
	}
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
