package commands

import (
	"context"
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/ocfp/ocfp-cli-go/internal/vaultboot"
)

// disableVaultBootUnit disables the bloc's ocfp-vault@<bloc>.service, which
// bastion init enables so a reboot brings the inception vault back. Once the
// vault is archived there is nothing for the unit to bring back.
//
// It disables the unit without stopping it. A unit that is not enabled, or a
// host without systemctl, such as a workstation, is left alone. sudo runs
// with -n, so a sudo that wants a password fails at once instead of waiting
// for an answer nobody gives.
func disableVaultBootUnit(ctx context.Context, bloc string, log *zap.SugaredLogger) error {
	if bloc == "" {
		return nil
	}

	unit, err := vaultboot.UnitName(bloc)
	if err != nil {
		return err
	}

	out, err := vaultOps.run(ctx, cleanupCommand{name: "systemctl", args: []string{"is-enabled", unit}})

	state := strings.TrimSpace(string(out))
	if state != "enabled" && state != "enabled-runtime" {
		log.Debugw("Inception vault boot unit is not enabled, so there is nothing to disable",
			"unit", unit, "state", state, "error", err)

		return nil
	}

	_, err = vaultOps.run(ctx, cleanupCommand{name: "sudo", args: []string{"-n", "systemctl", "disable", unit}})
	if err != nil {
		return fmt.Errorf("disabling %s: %w; disable it by hand with 'sudo systemctl disable %s'", unit, err, unit)
	}

	log.Infow("Inception vault boot unit disabled", "unit", unit)

	return nil
}
