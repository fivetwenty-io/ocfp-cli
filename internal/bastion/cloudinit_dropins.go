package bastion

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/fivetwenty-io/ocfp-cli/internal/guestboot"
)

// cloudInitDropIns are the cloud.cfg.d files that keep a PVE bastion the same
// machine across a change to its VM config. The template seed bakes them into
// every new template, but a bastion cloned from an older template has neither,
// or only the first, and nothing else would ever add them.
var cloudInitDropIns = []struct{ path, content string }{
	{guestboot.KeepHostKeysConfigPath, guestboot.KeepHostKeysConfig},
	{guestboot.TrustInstanceCacheConfigPath, guestboot.TrustInstanceCacheConfig},
}

const (
	dropInMode  = "644"
	dropInOwner = "root:root"
)

// dropInInstallScript writes one drop-in. It stages the decoded content in a
// file beside the destination, sets the owner and mode on that file, and then
// renames it into place, so the destination is always either the old file or
// the finished new one. A failure at any step removes the staged file. The
// script holds no single quote, so it is safe inside bash -c '...'.
const dropInInstallScript = `set -euo pipefail
tmp="$(sudo mktemp "$(dirname {{PATH}})/.ocfp-dropin.XXXXXX")"
trap "sudo rm -f -- \"$tmp\"" EXIT
echo {{CONTENT}} | base64 -d | sudo tee "$tmp" > /dev/null
sudo chown root:root "$tmp"
sudo chmod 0644 "$tmp"
sudo mv -f "$tmp" {{PATH}}`

// renderDropInInstall fills the install script in for one drop-in.
func renderDropInInstall(path, content string) string {
	return strings.NewReplacer(
		"{{CONTENT}}", base64.StdEncoding.EncodeToString([]byte(content)),
		"{{PATH}}", path,
	).Replace(dropInInstallScript)
}

// syncCloudInitDropIns is the form init runs. The drop-ins are a convenience, so
// a bastion that refuses them, for example because sudo asks for a password,
// still goes on to the rest of init. The error names the drop-in path, and the
// next run converges whatever is left.
func (m *Manager) syncCloudInitDropIns(ctx context.Context) error {
	err := m.convergeCloudInitDropIns(ctx)
	if err != nil {
		m.log.Warnw("Could not converge the cloud-init drop-ins, continuing without them",
			"error", err)
	}

	return nil
}

// convergeCloudInitDropIns writes each cloud-init drop-in on a PVE bastion
// that lacks it or holds a different copy, and leaves a matching one alone.
//
// It only writes files. cloud-init reads them at the next boot, so nothing is
// restarted, nothing reboots, and the host keys are not touched. AWS and GCP
// bastions are skipped, because their keep-host-keys drop-in comes from the
// user-data that built them, and a new instance-id there is a new machine
// that must not trust the cache.
//
// The dataset script and unit are not converged here, even though a bastion
// from an older template carries an older copy. Installing a working script
// on a bastion whose jq went missing would have the next boot restore the
// host keys stored on the data disk, and if those have drifted from the live
// keys, the host identity would flip. That has to be an operator's decision
// after comparing the two sets.
func (m *Manager) convergeCloudInitDropIns(ctx context.Context) error {
	if !strings.EqualFold(m.config.Provider, "pve") {
		m.log.Debugw("Not a PVE bastion, leaving the cloud-init drop-ins alone", "provider", m.config.Provider)

		return nil
	}

	if m.options != nil && m.options.DryRun {
		m.log.Infow("DRY RUN: Would converge the cloud-init drop-ins on the bastion")

		return nil
	}

	for _, dropIn := range cloudInitDropIns {
		err := m.convergeCloudInitDropIn(ctx, dropIn.path, dropIn.content)
		if err != nil {
			return err
		}
	}

	return nil
}

// convergeCloudInitDropIn writes content to path unless the bastion's copy
// already has the same checksum, mode, and owner. A probe that fails counts as a missing file,
// so the install runs and reports any real problem.
func (m *Manager) convergeCloudInitDropIn(ctx context.Context, path, content string) error {
	localSum := sha256.Sum256([]byte(content))
	localChecksum := hex.EncodeToString(localSum[:])

	// The probe prints the checksum, then the mode and owner. A missing file
	// prints nothing, which never matches.
	want := []string{localChecksum, dropInMode, dropInOwner}
	checkCmd := fmt.Sprintf("sha256sum '%[1]s' 2>/dev/null | awk '{print $1}'; stat -c '%%a %%U:%%G' '%[1]s' 2>/dev/null", path)

	checkResult, err := m.sshClient.ExecuteCommand(ctx, checkCmd)
	if err != nil {
		m.log.Debugw("Could not check the cloud-init drop-in's checksum", "path", path, "error", err)
	}

	if checkResult != nil && slices.Equal(strings.Fields(checkResult.Stdout), want) {
		m.log.Infow("cloud-init drop-in already in place, skipping", "path", path)

		return nil
	}

	script := renderDropInInstall(path, content)

	result, err := m.sshClient.ExecuteCommand(ctx, "bash -c '"+script+"'")
	if err != nil {
		stderr := ""
		if result != nil {
			stderr = extractTail(result.Stderr)
		}

		if stderr != "" {
			return fmt.Errorf("installing cloud-init drop-in %s: %w\n--- output ---\n%s", path, err, stderr)
		}

		return fmt.Errorf("installing cloud-init drop-in %s: %w", path, err)
	}

	m.log.Infow("cloud-init drop-in installed; it takes effect at the next boot",
		"path", path, "checksum", localChecksum)

	return nil
}
