package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// ErrMigrateInceptionVaultRunning is returned when a bloc that config migrate
// would move still has its inception vault running. Moving the bloc's
// directory would pull the vault's data and keys out from under the engine,
// so the command refuses before it moves anything.
var ErrMigrateInceptionVaultRunning = errors.New(
	"refusing to migrate while a bloc's inception vault is running; stop it first")

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
// this runs on a dry run as well.
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
