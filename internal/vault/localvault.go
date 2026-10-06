package vault

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"go.uber.org/zap"
)

const (
	// orphanEngineGracePeriod is how long an orphaned engine has to exit
	// after SIGTERM before it is sent SIGKILL.
	orphanEngineGracePeriod = 5 * time.Second

	// orphanEnginePollInterval is how often the grace period checks whether
	// the engine has let go of its data.
	orphanEnginePollInterval = time.Second
)

// localInceptionVaultPort returns the port the named bloc's workstation-local
// inception vault listens on (see getVaultInceptionPaths in internal/commands).
// It is bloc-scoped so teardown never evicts a sibling bloc's vault.
func localInceptionVaultPort(blocName string) string {
	return strconv.Itoa(config.InceptionVaultPort(blocName))
}

// runLocalCommand is the subprocess seam for local teardown operations.
// Tests override it to record invocations without spawning processes.
//
//nolint:gochecknoglobals // intentional seam for testing, mirrors sleepFn
var runLocalCommand = func(ctx context.Context, name string, args ...string) error {
	// #nosec G204 - callers pass fixed binaries (tmux/pkill/safe) and a
	// session/target name validated by isValidTmuxSession
	return exec.CommandContext(ctx, name, args...).Run()
}

// localCommandOutput is the seam for the read-only lookups teardown makes,
// such as lsof. Tests override it to script what the lookups find.
//
//nolint:gochecknoglobals // intentional seam for testing, mirrors runLocalCommand
var localCommandOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	// #nosec G204 - callers pass lsof with a port number or a path derived
	// from the validated bloc name
	return exec.CommandContext(ctx, name, args...).Output()
}

// localInceptionVaultDB returns the raft database of the bloc's
// workstation-local inception vault (see getVaultInceptionPaths in
// internal/commands).
func localInceptionVaultDB(blocName string) string {
	if blocName != "" {
		return filepath.Join(config.OcfpBlocDir(blocName), "vault", "data", "vault.db")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return filepath.Join(home, ".vault", "vault.db")
}

// lsofPIDs lists the PIDs lsof prints for args. lsof exits 1 with no output
// when it finds nothing, which is an empty answer; ok is false when lsof
// could not answer at all.
func lsofPIDs(ctx context.Context, args ...string) ([]string, bool) {
	out, err := localCommandOutput(ctx, "lsof", args...)
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || strings.TrimSpace(string(out)) != "" {
			return nil, false
		}
	}

	var pids []string

	for line := range strings.SplitSeq(string(out), "\n") {
		pid := strings.TrimSpace(line)
		if _, convErr := strconv.Atoi(pid); convErr == nil && pid != "" {
			pids = append(pids, pid)
		}
	}

	return pids, true
}

// killOrphanedInceptionEngine stops an engine that outlived its safe: one
// still listening on the bloc's API port and holding the bloc's raft data.
// A listener counts only when it also holds this bloc's vault.db, so a
// sibling bloc's vault on a colliding port, or anything else listening
// there, is never touched, and neither is this process. When lsof cannot
// answer, or there is no raft data to tie a listener to the bloc, nothing is
// killed. The engine gets SIGTERM, a grace period to let go of its data, and
// then SIGKILL.
func killOrphanedInceptionEngine(ctx context.Context, port, vaultDB string, log *zap.SugaredLogger) {
	if vaultDB == "" {
		return
	}

	listeners, ok := lsofPIDs(ctx, "-nP", "-t", "-iTCP:"+port, "-sTCP:LISTEN")
	if !ok || len(listeners) == 0 {
		return
	}

	holders, ok := lsofPIDs(ctx, "-t", vaultDB)
	if !ok {
		return
	}

	self := strconv.Itoa(os.Getpid())

	var orphans []string

	for _, pid := range listeners {
		if pid != self && slices.Contains(holders, pid) {
			orphans = append(orphans, pid)
		}
	}

	for _, pid := range orphans {
		log.Infow("Stopping an orphaned inception vault engine", "pid", pid, "port", port)

		//nolint:noinlineerr // errors are passed to the logger for context
		if err := runLocalCommand(ctx, "kill", "-TERM", pid); err != nil {
			log.Debugw("Orphaned engine did not take SIGTERM", "pid", pid, "error", err)
		}
	}

	for waited := time.Duration(0); len(orphans) > 0 && waited < orphanEngineGracePeriod; waited += orphanEnginePollInterval {
		sleepFn(orphanEnginePollInterval)

		holders, ok = lsofPIDs(ctx, "-t", vaultDB)
		if !ok {
			continue
		}

		orphans = slices.DeleteFunc(orphans, func(pid string) bool { return !slices.Contains(holders, pid) })
	}

	for _, pid := range orphans {
		log.Warnw("Orphaned inception vault engine ignored SIGTERM; killing it", "pid", pid, "port", port)

		//nolint:noinlineerr // errors are passed to the logger for context
		if err := runLocalCommand(ctx, "kill", "-KILL", pid); err != nil {
			log.Debugw("Orphaned engine is already gone", "pid", pid, "error", err)
		}
	}
}

// TeardownLocalInception decommissions the inception vault running on the
// host executing this process. It is called by `ocfp init bastion` (which
// runs on the operator workstation) once the bastion-side inception vault
// is up, populated, and verified — from that point the workstation vault is
// obsolete and its tmux session would otherwise linger forever, since
// `ocfp vault migrate` runs on the bastion and can never reach it.
//
// It stops the tmux session, kills local `safe local` proxy processes on
// the inception port, stops an engine that outlived its safe while still
// holding the bloc's raft data, and deletes the now-stale local safe target. It
// deliberately does NOT remove the vault data directory: that data is the
// last local copy of bootstrap-era secrets and is kept as a snapshot.
//
// All steps are best-effort; a session or target that is already gone is
// not an error.
func TeardownLocalInception(ctx context.Context, blocName string) error {
	session := "inception-vault"
	target := "inception"

	if blocName != "" {
		session = blocName + "-inception-vault"
		target = blocName + "-inception"
	}

	if !isValidTmuxSession(session) {
		return fmt.Errorf("%w: %s", ErrInvalidTmuxSession, session)
	}

	port := localInceptionVaultPort(blocName)

	log := logger.Get()
	log.Infow("Decommissioning local inception vault", "session", session, "target", target, "port", port)

	//nolint:noinlineerr // errors are passed to the logger for context
	if err := runLocalCommand(ctx, "tmux", "kill-session", "-t", session); err != nil {
		log.Debugw("Local inception vault tmux session not running", "session", session, "error", err)
	}

	//nolint:noinlineerr // errors are passed to the logger for context
	if err := runLocalCommand(ctx, "pkill", "-f", "safe local.*--port "+port); err != nil {
		log.Debugw("No local safe processes to kill", "port", port, "error", err)
	}

	killOrphanedInceptionEngine(ctx, port, localInceptionVaultDB(blocName), log)

	//nolint:noinlineerr // errors are passed to the logger for context
	if err := runLocalCommand(ctx, "safe", "target", "delete", target); err != nil {
		log.Debugw("Local safe target not present", "target", target, "error", err)
	}

	log.Infow("Local inception vault decommissioned", "session", session)

	return nil
}
