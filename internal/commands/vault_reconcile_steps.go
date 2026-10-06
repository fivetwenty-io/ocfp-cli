package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ErrVaultOwnerUnknown reports that lsof could not say which process listens
// on the inception vault port, so ocfp cannot tell whose vault it is.
var ErrVaultOwnerUnknown = errors.New("cannot tell whose vault holds the inception vault port")

// newInceptionSteps wires the real steps for one run, using the safe and
// engine binaries the prerequisite check validated.
func newInceptionSteps(tools inceptionTools) inceptionSteps {
	return inceptionSteps{
		probe:      probeInceptionVault,
		hasSession: inceptionSessionExists,
		ownsVault:  ownsInceptionVault,
		targetRegistered: func(ctx context.Context, paths map[string]string) bool {
			return inceptionTargetRegistered(ctx, tools.safe, paths)
		},
		retarget: func(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error {
			return retargetInceptionVault(ctx, tools.safe, paths, log)
		},
		stop:            stopInceptionVault,
		clusterPortFree: inceptionClusterPortFree,
		start:           startInceptionVault,
		finish: func(ctx context.Context, paths map[string]string, mode safeLocalMode, log *zap.SugaredLogger) error {
			return finishInceptionVault(ctx, tools.safe, paths, mode, log)
		},
		migrate: migrateFileVaultToRaft,
		now:     time.Now,
	}
}

// inceptionSessionExists reports whether the bloc's tmux session exists.
func inceptionSessionExists(ctx context.Context, paths map[string]string) bool {
	_, err := vaultOps.run(ctx, cleanupCommand{
		name: "tmux", args: []string{"has-session", "-t", paths["tmuxSession"]}, tmux: true,
	})

	return err == nil
}

// ownsInceptionVault reports whether the vault answering on the bloc's port is
// the bloc's own. Derived ports can collide between blocs, so a vault on the
// port may be a healthy sibling, and stopping it would take that bloc down.
//
// A raft engine keeps vault.db locked while it runs, so the vault is this
// bloc's exactly when the process listening on the port holds this bloc's
// vault.db. Without raft data there is no lock to compare, and the bloc's own
// tmux session is the evidence instead.
func ownsInceptionVault(ctx context.Context, paths map[string]string, data vaultDataState) (bool, error) {
	if data != vaultDataRaft {
		return inceptionSessionExists(ctx, paths), nil
	}

	listeners, err := portListeners(ctx, paths["port"])
	if err != nil {
		return false, err
	}

	holders, err := vaultDataHolders(ctx, filepath.Join(paths["vaultDir"], vaultRaftDBFile))
	if err != nil {
		return false, err
	}

	for _, pid := range listeners {
		if slices.Contains(holders, pid) {
			return true, nil
		}
	}

	return false, nil
}

// portListeners lists the processes listening on the loopback port. lsof
// exits 1 with no output when nothing listens; any other failure is not an
// answer.
func portListeners(ctx context.Context, port string) ([]string, error) {
	out, err := vaultOps.run(ctx, cleanupCommand{
		name: "lsof", args: []string{"-nP", "-t", "-iTCP:" + port, "-sTCP:LISTEN"},
	})
	if err != nil && !lsofFoundNobody(err, out) {
		return nil, fmt.Errorf("%w: lsof could not list listeners on port %s: %w", ErrVaultOwnerUnknown, port, err)
	}

	var pids []string

	for line := range strings.SplitSeq(string(out), "\n") {
		pid := strings.TrimSpace(line)
		if isPID(pid) {
			pids = append(pids, pid)
		}
	}

	return pids, nil
}

// inceptionClusterPortFree binds the cluster port on loopback, the address
// safe gives the engine, and releases it at once. A port that cannot be bound
// now would fail the engine later, after the old vault was already stopped.
func inceptionClusterPortFree(ctx context.Context, port string) error {
	var lc net.ListenConfig

	ln, err := lc.Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		return fmt.Errorf("cannot bind it: %w", err)
	}

	return ln.Close()
}

// safeTarget is one entry of `safe targets --json`.
type safeTarget struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// inceptionTargetRegistered reports whether safe has the bloc's target and
// that it points at the bloc's port. It reads every target, never the current
// one, because the current target is global and any sibling can move it.
func inceptionTargetRegistered(ctx context.Context, safePath string, paths map[string]string) bool {
	out, err := vaultOps.run(ctx, cleanupCommand{name: safePath, args: []string{"targets", "--json"}})
	if err != nil {
		return false
	}

	var targets []safeTarget

	if json.Unmarshal(out, &targets) != nil {
		return false
	}

	url := "http://127.0.0.1:" + paths["port"]

	return slices.ContainsFunc(targets, func(target safeTarget) bool {
		return target.Name == paths["vaultName"] && strings.TrimRight(target.URL, "/") == url
	})
}

// retargetInceptionVault registers the bloc's target again and authenticates
// it with the token in root.key. safe reads the token from stdin when stdin
// is not a terminal, so the token never reaches a command line, and safe's
// output is never shown because it can echo what it read.
func retargetInceptionVault(ctx context.Context, safePath string, paths map[string]string, log *zap.SugaredLogger) error {
	url := "http://127.0.0.1:" + paths["port"]

	err := exec.CommandContext(ctx, safePath, "target", paths["vaultName"], url).Run() // #nosec G204 -- safePath is the validated safe binary and the args come from getVaultInceptionPaths()
	if err != nil {
		return fmt.Errorf("safe could not register target %s at %s: %w", paths["vaultName"], url, err)
	}

	token, err := os.Open(paths["rootKeyFile"])
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", paths["rootKeyFile"], err)
	}

	defer func() { _ = token.Close() }()

	auth := exec.CommandContext(ctx, safePath, "-T", paths["vaultName"], "auth", "token") // #nosec G204 -- safePath is the validated safe binary and the args come from getVaultInceptionPaths()
	auth.Stdin = token

	err = auth.Run()
	if err != nil {
		return fmt.Errorf("safe could not authenticate target %s with the token in %s: %w",
			paths["vaultName"], paths["rootKeyFile"], err)
	}

	log.Infow("Registered the inception vault target again", "target", paths["vaultName"], "url", url)

	return nil
}

// startInceptionVault launches safe local in mode and waits until it reports
// the vault ready, or reports why it is not.
func startInceptionVault(
	ctx context.Context, paths map[string]string, tools inceptionTools, mode safeLocalMode, log *zap.SugaredLogger,
) error {
	err := prepareVaultDirectories(paths, log)
	if err != nil {
		return err
	}

	err = startVaultInTmux(ctx, paths, tools, mode, log)
	if err != nil {
		return err
	}

	return waitForVaultReady(ctx, paths, log)
}

// finishInceptionVault targets a vault safe just started and, for a new
// vault, saves the keys it printed. A restarted vault was opened with the
// saved keys, so they are left exactly as they are.
func finishInceptionVault(
	ctx context.Context, safePath string, paths map[string]string, mode safeLocalMode, log *zap.SugaredLogger,
) error {
	err := targetInceptionVault(ctx, safePath, paths, log)
	if err != nil {
		return fmt.Errorf("failed to target vault: %w", err)
	}

	if mode == safeLocalFresh {
		saveErr := saveVaultKeys(ctx, paths, log)
		if saveErr != nil {
			log.Warnw("Failed to save vault keys", "error", saveErr)
		}

		if !inceptionKeysUsable(paths) {
			log.Errorw("The new inception vault's keys were not both saved; it cannot be reopened after it stops",
				"root_token", paths["rootKeyFile"], "unseal_key", paths["unsealKeysFile"])
		}
	}

	log.Info("=== Vault Inception Completed Successfully ===")
	printVaultInfo(paths, log)

	return nil
}

// setAsidePreviousVaultLog moves the last run's log to <log>.previous before
// a start, replacing any older one. The wait reads the log, and the last
// run's "Now targeting" would pass for ready, while its rejected-token line
// would archive a vault whose keys are fine.
func setAsidePreviousVaultLog(logFile string) error {
	err := os.Rename(logFile, logFile+".previous")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("failed to set the previous inception vault log aside: %w", err)
	}

	return nil
}
