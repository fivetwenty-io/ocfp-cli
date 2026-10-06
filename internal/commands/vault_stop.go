package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

const (
	// vaultStopAttempts is how many times stopInceptionVault checks, a
	// vaultStopPollInterval apart, that the port and the data are free.
	vaultStopAttempts = 15

	// vaultStopEscalateAfter is the attempt from which a process still
	// holding the data is sent SIGKILL.
	vaultStopEscalateAfter = 10

	// vaultStopPollInterval is the pause between stop checks.
	vaultStopPollInterval = time.Second

	// vaultPortDialTimeout bounds the connect used to see whether the API
	// port still has a listener.
	vaultPortDialTimeout = time.Second

	// vaultRaftDBFile is the bbolt file a raft-backed engine keeps locked
	// for as long as it runs.
	vaultRaftDBFile = "vault.db"
)

var (
	// ErrVaultWouldNotStop reports an inception vault whose port or data was
	// still held after the stop plan ran.
	ErrVaultWouldNotStop = errors.New("inception vault did not stop")

	// ErrVaultLockUnknown reports that lsof could not say whether a process
	// still holds the raft database, so the vault cannot be known to be down.
	ErrVaultLockUnknown = errors.New("cannot tell whether the inception vault data is still in use")
)

// lsofNoMatchExit is the status lsof exits with when no process has the
// named file open.
const lsofNoMatchExit = 1

// inceptionVaultOps is the seam between the inception vault code and the
// host. Tests swap it for a fake that records commands and scripts answers,
// so the stop plan can be exercised without tmux, lsof, or a real engine.
type inceptionVaultOps struct {
	run      func(ctx context.Context, spec cleanupCommand) ([]byte, error)
	portOpen func(ctx context.Context, port string) bool
	sleep    func(d time.Duration)
}

//nolint:gochecknoglobals // test seam, replaced only by installFakeVaultOps
var vaultOps = inceptionVaultOps{
	run:      runVaultCommand,
	portOpen: localPortAcceptsConnections,
	sleep:    time.Sleep,
}

// runVaultCommand runs one planned command and returns its standard output.
func runVaultCommand(ctx context.Context, spec cleanupCommand) ([]byte, error) {
	cmd := exec.CommandContext(ctx, spec.name, spec.args...) // #nosec G204 -- args come from the controlled getVaultInceptionPaths() function
	if spec.tmux {
		ensureTmuxEnv(cmd)
	}

	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s failed: %w", spec.name, err)
	}

	return out, nil
}

// localPortAcceptsConnections reports whether anything is listening on the
// loopback port.
func localPortAcceptsConnections(ctx context.Context, port string) bool {
	dialer := net.Dialer{Timeout: vaultPortDialTimeout}

	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		return false
	}

	_ = conn.Close()

	return true
}

// stopInceptionVault stops this bloc's inception vault and waits until it
// has really gone, and never touches its keys or data.
//
// Killing safe is not enough on its own. safe runs the engine as a child, and
// a SIGKILL to safe orphans that child, which keeps the API port and, under
// raft, the bbolt lock on vault.db. A fresh start or a restart on top of that
// either fails to bind or hangs on the lock, so the stop polls until the port
// refuses connections and lsof finds nobody holding vault.db, and sends
// SIGKILL to whatever still holds the data once the grace period ends.
//
// The safe target is deleted only after the vault is down, because safe
// refuses to start a vault under a target name that already exists, and a
// vault that is still running should keep its target.
func stopInceptionVault(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error {
	log.Infow("Stopping inception vault", "session", paths["tmuxSession"], "port", paths["port"])

	for _, spec := range vaultCleanupCommands(paths) {
		_, _ = vaultOps.run(ctx, spec)
	}

	killVaultFromPIDFile(ctx, paths)

	dbPath := filepath.Join(paths["vaultDir"], vaultRaftDBFile)

	var (
		portOpen bool
		holders  []string
	)

	for attempt := range vaultStopAttempts {
		if ctx.Err() != nil {
			return fmt.Errorf("stopping inception vault: %w", ctx.Err())
		}

		portOpen = vaultOps.portOpen(ctx, paths["port"])

		var err error

		holders, err = vaultDataHolders(ctx, dbPath)
		if err != nil {
			return err
		}

		if !portOpen && len(holders) == 0 {
			for _, spec := range vaultCleanupTargetCommands(paths) {
				_, _ = vaultOps.run(ctx, spec)
			}

			log.Infow("Inception vault stopped", "session", paths["tmuxSession"])

			return nil
		}

		if attempt >= vaultStopEscalateAfter && len(holders) > 0 {
			log.Warnw("Sending SIGKILL to processes still holding the vault data", "pids", holders, "file", dbPath)

			_, _ = vaultOps.run(ctx, cleanupCommand{name: "kill", args: append([]string{"-9"}, holders...)})
		}

		vaultOps.sleep(vaultStopPollInterval)
	}

	return vaultStillHeldError(paths["port"], portOpen, dbPath, holders)
}

// killVaultFromPIDFile kills the process named in the PID file left by the
// old background-process launcher, then removes the file.
func killVaultFromPIDFile(ctx context.Context, paths map[string]string) {
	pidData, err := os.ReadFile(paths["pidFile"])
	if err != nil {
		return
	}

	pid := strings.TrimSpace(string(pidData))
	if isPID(pid) {
		_, _ = vaultOps.run(ctx, cleanupCommand{name: "kill", args: []string{"-9", pid}})
	}

	_ = os.Remove(paths["pidFile"])
}

// vaultDataHolders lists the processes, other than this one, that have the
// raft database open. A file-backed vault has no vault.db and so no holders.
//
// lsof exits 1 with no output when nobody has the file open, which is the
// answer a stop waits for. Any other failure, including lsof being missing,
// says nothing about the lock, and taking it for "nobody" would let a restart
// or an archive run under an orphaned engine. It is reported as
// ErrVaultLockUnknown instead.
func vaultDataHolders(ctx context.Context, dbPath string) ([]string, error) {
	_, err := os.Lstat(dbPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	out, err := vaultOps.run(ctx, cleanupCommand{name: "lsof", args: []string{"-t", dbPath}})
	if err != nil && !lsofFoundNobody(err, out) {
		return nil, fmt.Errorf("%w: lsof could not check %s: %w", ErrVaultLockUnknown, dbPath, err)
	}

	self := strconv.Itoa(os.Getpid())

	var pids []string

	for line := range strings.SplitSeq(string(out), "\n") {
		pid := strings.TrimSpace(line)
		if isPID(pid) && pid != self {
			pids = append(pids, pid)
		}
	}

	return pids, nil
}

// lsofFoundNobody reports whether an lsof failure is only its way of saying
// that no process has the file open.
func lsofFoundNobody(err error, out []byte) bool {
	var exitErr *exec.ExitError

	return errors.As(err, &exitErr) && exitErr.ExitCode() == lsofNoMatchExit &&
		strings.TrimSpace(string(out)) == ""
}

// isPID reports whether s is a positive decimal process ID. lsof can mix
// warnings into its output, and only a bare number may ever reach kill.
func isPID(s string) bool {
	if s == "" {
		return false
	}

	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}

	n, err := strconv.Atoi(s)

	return err == nil && n > 0
}

// vaultStillHeldError says what kept the vault from stopping.
func vaultStillHeldError(port string, portOpen bool, dbPath string, holders []string) error {
	var held []string

	if portOpen {
		held = append(held, "port "+port+" still accepts connections")
	}

	if len(holders) > 0 {
		held = append(held, dbPath+" is still held by PIDs "+strings.Join(holders, ", "))
	}

	return fmt.Errorf("%w after %d seconds: %s", ErrVaultWouldNotStop,
		int((vaultStopAttempts * vaultStopPollInterval).Seconds()), strings.Join(held, "; "))
}
