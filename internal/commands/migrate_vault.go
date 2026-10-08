package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// ErrMigrateInceptionVaultRunning is returned when a bloc that config migrate
// would move still has its inception vault running. Moving the bloc's
// directory would pull the vault's data and keys out from under the engine,
// so the command refuses before it moves anything.
var ErrMigrateInceptionVaultRunning = errors.New(
	"refusing to migrate while a bloc's inception vault is running; stop it first")

// ErrMigrateInceptionVaultBusy is returned when another ocfp run holds the
// inception vault lock of a bloc that config migrate would move. That run is
// working on the bloc's vault, and moving the bloc's directory would pull
// the vault's data and keys out from under it.
var ErrMigrateInceptionVaultBusy = errors.New(
	"refusing to migrate while another ocfp run works on a bloc's inception vault")

// migrateBlocLockTimeout bounds how long config migrate waits for each
// bloc's inception vault lock. It is short, because a run that holds the
// lock may hold it for minutes, and config migrate refuses rather than wait
// for one.
var migrateBlocLockTimeout = 5 * time.Second //nolint:gochecknoglobals // tests shorten the wait

// inceptionLivenessProbes are the checks config migrate uses to tell whether
// a bloc's inception vault is up. They are the same probes ocfp vault start
// and the inception reconcile use, gathered here so tests can swap in fakes.
type inceptionLivenessProbes struct {
	// probe reports what answers on the vault's API address.
	probe func(ctx context.Context, addr string) vaultProbe
	// hasSession reports whether the bloc's tmux session exists.
	hasSession func(ctx context.Context, paths map[string]string) bool
}

// migrateVaultProbes are the liveness probes config migrate runs against
// every bloc it would move. Tests replace them.
var migrateVaultProbes = inceptionLivenessProbes{ //nolint:gochecknoglobals // test seam
	probe:      probeInceptionVault,
	hasSession: inceptionSessionExists,
}

// blocInceptionEndpoint returns the port and tmux session of a bloc's
// inception vault, under the same keys getVaultInceptionPaths uses. It does
// not resolve the bloc's directory, because the liveness of a vault does not
// depend on where its files are, and a bloc whose directory cannot be
// resolved can still have a vault running.
func blocInceptionEndpoint(blocName string) map[string]string {
	return map[string]string{
		"port":        strconv.Itoa(config.InceptionVaultPort(blocName)),
		"tmuxSession": blocName + "-inception-vault",
	}
}

// runningBlocVault records what the probes found for one bloc.
type runningBlocVault struct {
	bloc    string
	port    string
	session string
	probe   vaultProbeState
	tmux    bool
}

// refuseIfBlocVaultsRunning probes the inception vault of every bloc
// directory in plan that may hold a vault/, by its API port and its tmux
// session, and refuses when any of them is up. Anything answering on the
// port counts, whether or not it is a vault, because a vault that is sealed,
// starting, or slow to answer can look like a stranger, and the session
// alone counts too, because it is how ocfp runs the vault. Every bloc that
// is up is named, so one refusal covers them all. The probes only read, so
// this runs on a dry run as well. Whether a bloc may hold a vault/ is looked
// up afresh on every call, so the call config migrate makes under the
// blocs' locks also probes a bloc whose vault/ appeared after the first.
func refuseIfBlocVaultsRunning(ctx context.Context, plan []migratePlanEntry, probes inceptionLivenessProbes) error {
	var running []runningBlocVault

	for _, entry := range plan {
		if entry.class != migrateClassDataOther || !mayHoldBlocVault(entry.src) {
			continue
		}

		endpoint := blocInceptionEndpoint(entry.name)
		found := runningBlocVault{
			bloc:    entry.name,
			port:    endpoint["port"],
			session: endpoint["tmuxSession"],
			probe:   probes.probe(ctx, "http://127.0.0.1:"+endpoint["port"]).state,
			tmux:    probes.hasSession(ctx, endpoint),
		}

		if found.probe != vaultProbeStopped || found.tmux {
			running = append(running, found)
		}
	}

	if len(running) == 0 {
		return nil
	}

	lines := make([]string, 0, len(running))
	for _, vault := range running {
		lines = append(lines, "  "+vault.describe())
	}

	return fmt.Errorf("%w. Nothing was moved. These blocs still have their inception vault up:\n%s\n"+
		"Stop each one with 'tmux kill-session -t <bloc>-inception-vault', or stop whatever process "+
		"'lsof -nP -iTCP:<port> -sTCP:LISTEN' names when there is no session. Once nothing answers on "+
		"the port, run 'ocfp config migrate' again, and then bring the vault back with "+
		"'ocfp vault start --bloc <bloc>'",
		ErrMigrateInceptionVaultRunning, strings.Join(lines, "\n"))
}

// mayHoldBlocVault reports whether a legacy data directory could hold an
// inception vault. Only a missing vault/ rules that out; any other failure
// to look still counts, so the directory is probed.
func mayHoldBlocVault(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, "vault"))

	return !errors.Is(err, fs.ErrNotExist)
}

// describe says what was found for the bloc and how to stop it.
func (v runningBlocVault) describe() string {
	var found []string

	switch v.probe {
	case vaultProbeVault:
		found = append(found, "a vault answers on port "+v.port)
	case vaultProbeStranger:
		found = append(found, "something answers on port "+v.port)
	case vaultProbeStopped:
	}

	if v.tmux {
		found = append(found, "tmux session "+v.session+" exists")
	}

	stop := "'tmux kill-session -t " + v.session + "'"
	if !v.tmux {
		stop += ", or stop the process that 'lsof -nP -iTCP:" + v.port + " -sTCP:LISTEN' names"
	}

	return fmt.Sprintf("bloc %s: %s; stop it with %s", v.bloc, strings.Join(found, " and "), stop)
}

// migratingBlocVaults returns, in name order, every bloc directory in plan,
// which are the blocs whose locks config migrate holds while it moves them.
// A bloc with no vault/ is locked too, because a vault command that resolves
// its legacy directory would start a vault there while it moved.
func migratingBlocVaults(plan []migratePlanEntry) []string {
	var blocs []string

	for _, entry := range plan {
		if entry.class == migrateClassDataOther {
			blocs = append(blocs, entry.name)
		}
	}

	slices.Sort(blocs)

	return slices.Compact(blocs)
}

// withBlocVaultLocks runs fn while holding the inception vault lock of every
// bloc in blocs. The locks are taken in the order given, which callers keep
// sorted by name, and each wait is bounded by migrateBlocLockTimeout. When a
// lock stays held, fn never runs, the locks already taken are released, and
// the error wraps ErrMigrateInceptionVaultBusy and names the bloc. A lock
// that cannot be taken for any other reason, such as a permission error,
// ends the run the same way, but its error names that failure and does not
// wrap ErrMigrateInceptionVaultBusy, since no other run holds the lock.
//
// Every vault command takes its bloc's lock before it resolves the bloc's
// directory, so while these locks are held no vault command works on these
// blocs, and one that starts meanwhile waits and then resolves the
// directory the bloc moved to.
func withBlocVaultLocks(blocs []string, fn func() error) error {
	if len(blocs) == 0 {
		return fn()
	}

	bloc := blocs[0]
	lockFile := inceptionLockPath(bloc, false)
	acquired := false

	err := config.WithFileLock(lockFile, migrateBlocLockTimeout, func() error {
		acquired = true

		return withBlocVaultLocks(blocs[1:], fn)
	})
	if acquired || err == nil {
		return err
	}

	if errors.Is(err, config.ErrFileLockTimeout) {
		return fmt.Errorf("%w. Nothing was moved. Another ocfp run, such as 'ocfp vault inception' or "+
			"'ocfp vault start', holds bloc %s's inception vault lock, %s. Run 'ocfp config migrate' again once "+
			"it has finished: %w", ErrMigrateInceptionVaultBusy, bloc, lockFile, err)
	}

	return fmt.Errorf("refusing to migrate, because the inception vault lock of bloc %s, %s, could not be taken. "+
		"Nothing was moved. Fix the cause given at the end of this message, and then run 'ocfp config migrate' "+
		"again: %w", bloc, lockFile, err)
}
