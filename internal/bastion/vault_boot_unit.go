package bastion

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/provision"
	"github.com/ocfp/ocfp-cli-go/internal/vaultboot"
)

var (
	// errEmptyOperatorProbe reports a probe that did not print a user and a
	// home directory.
	errEmptyOperatorProbe = errors.New("the bastion did not report a user and a home directory")

	// errOCFPLacksVaultStart reports an installed ocfp binary that has no
	// 'vault start', which the unit runs at every boot.
	errOCFPLacksVaultStart = errors.New("the installed ocfp binary has no 'vault start' command")
)

// operatorProbeCommand prints the SSH user's name and home, one per line.
// The unit runs as that user, because the bloc's key files and safe's
// targets live under that home.
const operatorProbeCommand = `id -un && printf '%s\n' "$HOME"`

// installVaultBootUnit installs ocfp-vault@.service and the bloc instance's
// operator drop-in, and enables the instance, so the inception vault comes
// back after the bastion reboots. It first checks that the installed ocfp
// has 'vault start', because a unit that runs a missing command would fail
// at every boot. It enables the instance without starting it. vault_inception, which runs
// next, is what starts the vault during init, and the unit only runs
// 'ocfp vault start' at boot, which never archives or creates a vault.
func (m *Manager) installVaultBootUnit(ctx context.Context) error {
	bloc := m.config.Name

	unitName, err := vaultboot.UnitName(bloc)
	if err != nil {
		return fmt.Errorf("installing the inception vault boot unit: %w", err)
	}

	if m.options != nil && m.options.DryRun {
		m.log.Infow("DRY RUN: Would install and enable the inception vault boot unit",
			"unit", unitName, "path", vaultboot.UnitPath)

		return nil
	}

	err = m.requireVaultStart(ctx)
	if err != nil {
		return err
	}

	user, home, err := m.probeOperator(ctx)
	if err != nil {
		return err
	}

	unit, err := vaultboot.Render(vaultboot.UnitParams{
		User:     user,
		Home:     home,
		OCFPPath: provision.OCFPInstallPath,
	})
	if err != nil {
		return fmt.Errorf("rendering %s for %s: %w", vaultboot.TemplateName, user, err)
	}

	installCmd, err := vaultboot.InstallCommand(bloc, unit)
	if err != nil {
		return fmt.Errorf("installing the inception vault boot unit: %w", err)
	}

	result, err := m.sshClient.ExecuteCommand(ctx, installCmd)
	if err != nil {
		stderr := ""
		if result != nil {
			stderr = extractTail(result.Stderr)
		}

		if stderr != "" {
			return fmt.Errorf("installing %s and enabling %s: %w\n--- output ---\n%s",
				vaultboot.UnitPath, unitName, err, stderr)
		}

		return fmt.Errorf("installing %s and enabling %s: %w", vaultboot.UnitPath, unitName, err)
	}

	m.log.Infow("Inception vault boot unit installed and enabled",
		"unit", unitName, "user", user, "path", vaultboot.UnitPath)

	return nil
}

// requireVaultStart fails unless the installed ocfp binary has 'vault
// start'. It asks the binary through a hidden flag rather than reading its
// help, so a change of wording cannot fool it, and a binary that predates
// the command exits non-zero on the unknown flag.
func (m *Manager) requireVaultStart(ctx context.Context) error {
	result, err := m.sshClient.ExecuteCommand(ctx, vaultboot.SupportCheckCommand(provision.OCFPInstallPath))
	if err == nil {
		return nil
	}

	output := ""

	if result != nil {
		if tail := extractTail(strings.TrimSpace(result.Stderr + "\n" + result.Stdout)); tail != "" {
			output = "\n--- output ---\n" + tail
		}
	}

	return fmt.Errorf("%w: '%s' on the bastion failed (%w), so the boot unit would fail at every boot; "+
		"install an ocfp release that has 'ocfp vault start' and run init again%s",
		errOCFPLacksVaultStart, vaultboot.SupportCheckCommand(provision.OCFPInstallPath), err, output)
}

// probeOperator returns the SSH user's name and home directory on the
// bastion.
func (m *Manager) probeOperator(ctx context.Context) (string, string, error) {
	result, err := m.sshClient.ExecuteCommand(ctx, operatorProbeCommand)
	if err != nil {
		return "", "", fmt.Errorf("reading the bastion operator's user and home: %w", err)
	}

	if result == nil {
		return "", "", fmt.Errorf("reading the bastion operator's user and home: %w", errEmptyOperatorProbe)
	}

	// A login shell's profile can print before the probe does, so the user
	// and the home are the last two lines, and Render rejects anything that
	// is not a plain user name and an absolute path.
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) < 2 { //nolint:mnd // one line for the user, one for the home
		return "", "", fmt.Errorf("reading the bastion operator's user and home: %w", errEmptyOperatorProbe)
	}

	return strings.TrimSpace(lines[len(lines)-2]), strings.TrimSpace(lines[len(lines)-1]), nil
}
