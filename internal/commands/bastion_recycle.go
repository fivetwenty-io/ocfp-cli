package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/recycle"
)

// ErrArchiveStorageRequired is returned when --archive is given with no
// storage to write to. It fails before anything is stopped rather than
// halfway through, when the bastion would already be down.
var ErrArchiveStorageRequired = errors.New("--archive requires --archive-storage naming a backup-capable storage")

// recycleNeedsConfirmation reports whether the operator must confirm.
//
// A recycle passes a point of no return: once the original VM is destroyed its
// boot disk and its snapshots go with it, and the only route back is restoring
// an archive. An abort never needs confirmation, because it undoes rather than
// destroys.
func recycleNeedsConfirmation(yes, abort bool) bool {
	if abort {
		return false
	}

	return !yes
}

// recycleWarning is the text shown before anything is touched. It names the
// step that cannot be undone, because a warning that does not say which part
// is irreversible is not a warning.
func recycleWarning(blocName string) string {
	return fmt.Sprintf(`About to recycle the bastion for %s.

This stops the current bastion, takes a snapshot, builds a replacement from
the configured image, hands the data disk across, and then destroys the
original.

Destroying the original takes its boot disk and its snapshots with it, and
that step cannot be undone. Up to that point, `+"`--abort`"+` walks everything
back. After it, the only route back is restoring an archive.

Continue?`, blocName)
}

// validateRecycleFlags checks flag combinations that can only fail later.
func validateRecycleFlags(archive bool, archiveStorage string) error {
	if archive && strings.TrimSpace(archiveStorage) == "" {
		return ErrArchiveStorageRequired
	}

	return nil
}

// confirmRecycle prompts and reports whether the operator agreed.
func confirmRecycle(blocName string) bool {
	_, _ = fmt.Fprintln(os.Stdout, recycleWarning(blocName))
	_, _ = fmt.Fprint(os.Stdout, "Type the bloc name to proceed: ")

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return false
	}

	return strings.TrimSpace(scanner.Text()) == blocName
}

// runBastionRecycle drives the recycle engine for a bloc's bastion.
func runBastionRecycle(
	ctx context.Context,
	log logger.Logger,
	blocName, configFile string,
	opts recycleFlags,
) error {
	if blocName == "" {
		return ErrBlocIsRequired
	}

	mgr, err := buildBootstrapManager(ctx, blocName, configFile)
	if err != nil {
		return err
	}

	if mgr.NativeRecycle() {
		return runNativeBastionRecycle(ctx, mgr, blocName, opts)
	}

	if err = validateRecycleFlags(opts.archive, opts.archiveStorage); err != nil {
		return err
	}

	cluster := bootstrap.NewBastionRecycleCluster(mgr, opts.archiveStorage).WithSafetyCopyTaken(opts.safetyCopyTaken)
	engine := recycle.New(cluster, recycle.Options{
		Archive:          opts.archive,
		Role:             "bastion",
		StopBeforeRetire: opts.stopBeforeRetire,
	})

	if opts.abort {
		log.Info("Aborting the recycle in progress")

		return engine.Abort(ctx)
	}

	if recycleNeedsConfirmation(opts.yes, opts.abort) && !confirmRecycle(blocName) {
		_, _ = fmt.Fprintln(os.Stdout, "Aborted; nothing was changed.")

		return nil
	}

	return engine.Run(ctx)
}

// recycleFlags holds the resolved flags for a recycle run.
type recycleFlags struct {
	archive          bool
	archiveStorage   string
	yes              bool
	abort            bool
	stopBeforeRetire bool
	safetyCopyTaken  bool
}

// buildBootstrapManager assembles the bootstrap manager a recycle drives.
//
// It reuses the same config load, provider construction, and state manager
// that `ocfp bootstrap` uses, so the replacement is built by exactly the code
// path a fresh bloc would take rather than by a parallel one that drifts.
func buildBootstrapManager(ctx context.Context, blocName, configFile string) (*bootstrap.Manager, error) {
	cfg, err := loadBlocConfiguration(configFile, blocName)
	if err != nil {
		return nil, err
	}

	iaas, region, err := determineProviderAndRegion(cfg)
	if err != nil {
		return nil, err
	}

	provider, err := createProvider(ctx, iaas, buildProviderConfig(cfg, region))
	if err != nil {
		return nil, err
	}

	stateManager, err := createStateManager(blocName)
	if err != nil {
		return nil, err
	}

	if _, err = stateManager.Load(blocName); err != nil {
		return nil, fmt.Errorf("load bloc state: %w", err)
	}

	return bootstrap.NewManager(cfg, provider, stateManager, &bootstrap.Options{
		BlocName: blocName,
		Provider: iaas,
		Region:   region,
		Bastion:  true,
	}), nil
}

func nativeRecycleWarning(blocName string) string {
	return fmt.Sprintf(`About to refresh the bastion OS for %s.
The same instance, network interfaces, private addresses, and data disk are retained.
The stopped root receives a durable snapshot, then a replacement boot disk.
The original root is deleted only after the replacement guest is verified.
Use --stop-before-retire to pause reversibly; --abort restores the old root before retirement.
The snapshot remains available after completion. Ephemeral public IPs may change.
Continue?`, blocName)
}
func runNativeBastionRecycle(ctx context.Context, mgr *bootstrap.Manager, blocName string, opts recycleFlags) error {
	if strings.TrimSpace(opts.archiveStorage) != "" {
		return errors.New("native cloud recycle uses provider snapshots; --archive-storage is only for PVE")
	}

	cluster, err := bootstrap.NewNativeBastionRecycle(mgr)
	if err != nil {
		return err
	}

	if recycleNeedsConfirmation(opts.yes, opts.abort) {
		_, _ = fmt.Fprintln(os.Stdout, nativeRecycleWarning(blocName))
		_, _ = fmt.Fprint(os.Stdout, "Type the bloc name to proceed: ")

		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != blocName {
			_, _ = fmt.Fprintln(os.Stdout, "Aborted; nothing was changed.")

			return nil
		}
	}

	if err := cluster.Run(ctx, opts.abort, opts.stopBeforeRetire); err != nil {
		return err
	}

	if opts.abort {
		_, _ = fmt.Fprintln(os.Stdout, "Native recycle aborted; original root restored.")

		return nil
	}

	if opts.stopBeforeRetire {
		_, _ = fmt.Fprintln(os.Stdout, "Paused with the new root running and the original root retained. Guest boot and data mounts verified. Re-run without --stop-before-retire to retire the old root, or use --abort.")
	} else {
		_, _ = fmt.Fprintln(os.Stdout, "Native boot refresh complete; recovery snapshot retained.")
	}

	_, _ = fmt.Fprintf(os.Stdout, "Run ocfp bastion init --bloc %s to finish tool and Vault service initialization. Boot verification does not replace bastion init.\n", blocName)

	return nil
}
