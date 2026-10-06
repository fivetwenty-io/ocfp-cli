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

	"github.com/ocfp/ocfp-cli-go/internal/keyfile"
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
		migrate:     migrateFileVaultToRaft,
		recoverKeys: recoverInceptionKeys,
		targetToken: blocTargetToken,
		now:         time.Now,
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
// vault.db. Without raft data there is no lock to compare. The bloc's tmux
// session is not enough on its own, because the session's shell outlives
// safe, so the process listening on the port must also run under one of the
// session's panes.
func ownsInceptionVault(ctx context.Context, paths map[string]string, data vaultDataState) (bool, error) {
	if data != vaultDataRaft {
		return listenerRunsInSession(ctx, paths)
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

// maxOwnerAncestry bounds the walk from a listener up through its parents.
// The engine is safe's child, and safe runs in the pane's shell, sometimes
// under a subshell for its pipe into tee, so the pane is at most a few
// levels up.
const maxOwnerAncestry = 8

// listenerRunsInSession reports whether a process listening on the bloc's
// port descends from a pane of the bloc's tmux session. An engine that safe
// left behind has been adopted by init, and a sibling's engine descends from
// the sibling's own pane, so neither passes.
func listenerRunsInSession(ctx context.Context, paths map[string]string) (bool, error) {
	if !inceptionSessionExists(ctx, paths) {
		return false, nil
	}

	listeners, err := portListeners(ctx, paths["port"])
	if err != nil || len(listeners) == 0 {
		return false, err
	}

	panes, err := inceptionPanePIDs(ctx, paths)
	if err != nil {
		return false, err
	}

	for _, pid := range listeners {
		under, err := descendsFrom(ctx, pid, panes)
		if err != nil || under {
			return under, err
		}
	}

	return false, nil
}

// inceptionPanePIDs lists the PIDs of the processes that the bloc's session
// started in its panes. The "=" asks tmux for this exact session name rather
// than the first one that starts with it.
func inceptionPanePIDs(ctx context.Context, paths map[string]string) ([]string, error) {
	out, err := vaultOps.run(ctx, cleanupCommand{
		name: "tmux", args: []string{"list-panes", "-s", "-t", "=" + paths["tmuxSession"], "-F", "#{pane_pid}"}, tmux: true,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: tmux could not list the panes of session %s: %w",
			ErrVaultOwnerUnknown, paths["tmuxSession"], err)
	}

	var pids []string

	for line := range strings.SplitSeq(string(out), "\n") {
		pid := strings.TrimSpace(line)
		if isPID(pid) {
			pids = append(pids, pid)
		}
	}

	if len(pids) == 0 {
		return nil, fmt.Errorf("%w: tmux listed no panes for session %s", ErrVaultOwnerUnknown, paths["tmuxSession"])
	}

	return pids, nil
}

// descendsFrom reports whether pid, or one of its parents within
// maxOwnerAncestry levels, is one of ancestors. A process that exits during
// the walk descends from nothing. The walk ends at init, PID 1, which no
// pane can be.
func descendsFrom(ctx context.Context, pid string, ancestors []string) (bool, error) {
	for range maxOwnerAncestry + 1 {
		if slices.Contains(ancestors, pid) {
			return true, nil
		}

		if pid == "1" {
			return false, nil
		}

		parent, gone, err := parentPID(ctx, pid)
		if err != nil || gone {
			return false, err
		}

		pid = parent
	}

	return false, nil
}

// psNoProcessExit is the status ps exits with, printing nothing, when the
// process it was asked about does not exist.
const psNoProcessExit = 1

// parentPID returns the parent of pid. gone is true when pid no longer
// exists. Anything else ps prints, or any other failure, is not an answer.
func parentPID(ctx context.Context, pid string) (string, bool, error) {
	out, err := vaultOps.run(ctx, cleanupCommand{name: "ps", args: []string{"-o", "ppid=", "-p", pid}})

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == psNoProcessExit && strings.TrimSpace(string(out)) == "" {
		return "", true, nil
	}

	if err != nil {
		return "", false, fmt.Errorf("%w: ps could not find the parent of process %s: %w", ErrVaultOwnerUnknown, pid, err)
	}

	parent := strings.TrimSpace(string(out))
	if !isPID(parent) {
		return "", false, fmt.Errorf("%w: ps did not report a parent for process %s", ErrVaultOwnerUnknown, pid)
	}

	return parent, false, nil
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

		keys, keysErr := inceptionKeysSaved(paths)
		if keysErr != nil || !keys {
			log.Errorw("The new inception vault's keys were not both saved; it cannot be reopened after it stops",
				"root_token", paths["rootKeyFile"], "unseal_key", paths["unsealKeysFile"])

			return errors.Join(fmt.Errorf("%w: the vault is still running, and its full keys are in the vault log at %s",
				ErrInceptionKeysNotSaved, paths["logFile"]), keysErr)
		}
	}

	log.Info("=== Vault Inception Completed Successfully ===")
	printVaultInfo(paths, log)

	return nil
}

// previousVaultLogSuffix names the log of the start before the current one.
const previousVaultLogSuffix = ".previous"

// setAsidePreviousVaultLog moves the last run's log to <log>.previous before
// a start. The wait reads the log, and the last run's "Now targeting" would
// pass for ready, while its rejected-token line would archive a vault whose
// keys are fine.
//
// A log can hold the only copy of a new vault's unseal key, and the log sits
// outside <bloc>/vault, so an archive does not carry it. An existing
// .previous is therefore moved to .previous-<timestamp> first, and no log is
// ever replaced or deleted.
func setAsidePreviousVaultLog(logFile string) error {
	_, err := os.Lstat(logFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to inspect the previous inception vault log %s: %w", logFile, err)
	}

	previous := logFile + previousVaultLogSuffix

	_, err = os.Lstat(previous)

	switch {
	case err == nil:
		_, err = moveAsideUnderFreeName(previous, previous+"-"+time.Now().Format(vaultArchiveTimeFormat))
		if err != nil {
			return fmt.Errorf("failed to keep the older inception vault log: %w", err)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("failed to inspect the older inception vault log %s: %w", previous, err)
	}

	err = renameRefusingExisting(logFile, previous)
	if err != nil {
		return fmt.Errorf("failed to set the previous inception vault log aside: %w", err)
	}

	return nil
}

// recoverInceptionKeys writes whichever of the bloc's key files are missing or
// blank, from what its running vault left behind. The unseal key comes from
// the log safe local tees its output to, or failing that from the tmux pane's
// history. The root token comes from the bloc's own target in ~/.saferc, and
// only when that target points at the bloc's port; the global current target
// is never read, because any sibling bloc can move it. A key file that holds
// a value is never replaced, and one that exists but cannot be read is an
// error, since it may hold that value. Finding nothing is not an error: the
// vault then goes down the archive path.
func recoverInceptionKeys(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error {
	if paths["rootKeyFile"] == paths["unsealKeysFile"] {
		return nil
	}

	unseal, err := keyFileHasValue(paths["unsealKeysFile"])
	if err != nil {
		return fmt.Errorf("refusing to recover the unseal key: %w", err)
	}

	root, err := keyFileHasValue(paths["rootKeyFile"])
	if err != nil {
		return fmt.Errorf("refusing to recover the root token: %w", err)
	}

	if !unseal {
		sealKey := extractSealKey(runningVaultOutput(ctx, paths))
		if sealKey == "" {
			log.Warnw("The running inception vault's unseal key is not in its log", "log", paths["logFile"])
		} else {
			err := checkSealKey(sealKey)
			if err != nil {
				return fmt.Errorf("refusing to recover the unseal key in the vault's output into %s: %w",
					paths["unsealKeysFile"], err)
			}

			err = keyfile.WriteRecovered(paths["unsealKeysFile"], sealKey)
			if err != nil {
				return err
			}

			log.Infow("Recovered the unseal key from the running vault's output", "path", paths["unsealKeysFile"])
		}
	}

	if !root {
		token := blocTargetToken(paths)
		if token == "" {
			log.Warnw("No root token for the inception vault in ~/.saferc", "target", paths["vaultName"])

			return nil
		}

		err := keyfile.CheckRootToken(token)
		if err != nil {
			return fmt.Errorf("refusing to recover the root token of target %s into %s: %w",
				paths["vaultName"], paths["rootKeyFile"], err)
		}

		err = keyfile.WriteRecovered(paths["rootKeyFile"], token)
		if err != nil {
			return err
		}

		log.Infow("Recovered the root token from safe's target", "path", paths["rootKeyFile"], "target", paths["vaultName"])
	}

	return nil
}

// repairUnsealKeyFile restores a whole unseal key to a key file that holds
// only the start of one, the way older captures of a tmux pane cut the key
// at the pane's edge. The engine would refuse the cut key and the vault would
// be archived while its real key still sat in safe's output, so the key file
// is repaired from that output before anything starts: the tee'd log, the log
// of the run before it, and the pane's history.
//
// Only a key that begins with what the file holds is taken, so a key from
// some other vault is never written. When none is found, the disk is left
// exactly as it is and the error says so. A missing or blank key file is
// left to the archive path, as before, and one that cannot be read is an
// error. A whole key with stray whitespace around it is rewritten to the key
// and a newline, which is the only form safe passes on intact.
func repairUnsealKeyFile(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error {
	keyFile := paths["unsealKeysFile"]
	if paths["rootKeyFile"] == keyFile {
		return nil
	}

	data, err := keyfile.ReadBytes(keyFile)
	if err != nil {
		return err
	}

	held := strings.TrimSpace(string(data))
	if held == "" {
		return nil
	}

	shapeErr := checkSealKey(held)
	if shapeErr == nil {
		return canonicalizeKeyFile(keyFile, data, held, log)
	}

	var found []string

	for _, output := range unsealKeySources(ctx, paths) {
		for _, key := range sealKeysIn(output) {
			if strings.HasPrefix(key, held) && !slices.Contains(found, key) {
				found = append(found, key)
			}
		}
	}

	if len(found) != 1 {
		return fmt.Errorf("%w: %s: %w, and safe's output in %s, %s.previous, and the tmux session %s "+
			"holds no single whole key that it is the start of, so nothing was started, stopped, or changed; "+
			"put the vault's whole unseal key in that file and run this again",
			ErrUnsealKeyFileMalformed, keyFile, shapeErr, paths["logFile"], paths["logFile"], paths["tmuxSession"])
	}

	err = keyfile.WriteRecovered(keyFile, found[0])
	if err != nil {
		return err
	}

	log.Warnw("Restored the whole unseal key to a key file that held only part of it", "path", keyFile)

	return nil
}

// canonicalizeKeyFile rewrites a key file whose trimmed content is a whole
// key, but whose bytes are not exactly that key and a newline. safe reads only
// the first line of what it is fed and strips only the line ending, so a key
// behind a blank line or beside a stray space reaches the engine as a key it
// refuses, and a refused key archives the vault. A file already in that form
// is left untouched.
func canonicalizeKeyFile(keyFile string, data []byte, key string, log *zap.SugaredLogger) error {
	if string(data) == key+"\n" {
		return nil
	}

	err := keyfile.WriteRecovered(keyFile, key)
	if err != nil {
		return err
	}

	log.Warnw("Rewrote a key file that held its key with stray whitespace around it", "path", keyFile)

	return nil
}

// canonicalizeRootKeyFile gives root.key the treatment canonicalizeKeyFile
// gives the unseal key. Re-targeting a running vault feeds root.key to safe
// on stdin, where only the first line counts. A token that is not the shape
// of a token is left as it is, for the engine to judge.
func canonicalizeRootKeyFile(paths map[string]string, log *zap.SugaredLogger) error {
	keyFile := paths["rootKeyFile"]
	if keyFile == paths["unsealKeysFile"] {
		return nil
	}

	data, err := keyfile.ReadBytes(keyFile)
	if err != nil {
		return err
	}

	token := strings.TrimSpace(string(data))
	if token != "" && keyfile.CheckRootToken(token) == nil {
		return canonicalizeKeyFile(keyFile, data, token, log)
	}

	return nil
}

// recoverUnsealKeyFromLogs writes a missing or blank unseal.keys of a
// stopped vault from the whole unseal key that safe printed into the vault
// log, or into the log of the start before it. Without it the vault would be
// archived for a missing key while its key still sat in a log.
//
// The newest log that holds a whole key is used, since a new vault prints
// its key once, on the start that created the data now on disk. A log that
// exists but cannot be read is an error, and nothing is written then.
// Finding no key leaves the disk as it was.
func recoverUnsealKeyFromLogs(paths map[string]string, log *zap.SugaredLogger) error {
	keyFile := paths["unsealKeysFile"]
	if paths["rootKeyFile"] == keyFile {
		return nil
	}

	held, err := keyFileHasValue(keyFile)
	if err != nil || held {
		return err
	}

	for _, logFile := range vaultLogFiles(paths) {
		data, readErr := os.ReadFile(logFile) // #nosec G304 -- the bloc's own log files from getVaultInceptionPaths()
		if errors.Is(readErr, fs.ErrNotExist) {
			continue
		}

		if readErr != nil {
			return fmt.Errorf("cannot read %s, which may hold the unseal key %s is missing; "+
				"nothing was started or archived: %w", logFile, keyFile, readErr)
		}

		keys := sealKeysIn(stripANSI(string(data)))
		if len(keys) == 0 {
			continue
		}

		err = keyfile.WriteRecovered(keyFile, keys[len(keys)-1])
		if err != nil {
			return err
		}

		log.Warnw("Recovered the stopped vault's missing unseal key from its log", "path", keyFile, "log", logFile)

		return nil
	}

	return nil
}

// vaultLogFiles lists the bloc's vault log and the log of the start before
// it, newest first.
func vaultLogFiles(paths map[string]string) []string {
	return []string{paths["logFile"], paths["logFile"] + previousVaultLogSuffix}
}

// unsealKeySources returns what safe printed for the bloc's vault, from the
// tee'd log, the previous run's log, and the tmux pane's whole history.
func unsealKeySources(ctx context.Context, paths map[string]string) []string {
	var sources []string

	for _, logFile := range vaultLogFiles(paths) {
		data, err := os.ReadFile(logFile) // #nosec G304 -- the bloc's own log files from getVaultInceptionPaths()
		if err == nil {
			sources = append(sources, stripANSI(string(data)))
		}
	}

	pane, err := vaultOps.run(ctx, cleanupCommand{
		name: "tmux", args: capturePaneArgs(paths["tmuxSession"], "-"), tmux: true,
	})
	if err == nil {
		sources = append(sources, stripANSI(string(pane)))
	}

	return sources
}

// sealKeysIn returns every whole unseal key in safe's output, in the order
// printed, and skips any that is not the shape of a key.
func sealKeysIn(output string) []string {
	var keys []string

	for line := range strings.Lines(output) {
		key := extractSealKey(line)
		if key != "" && checkSealKey(key) == nil {
			keys = append(keys, key)
		}
	}

	return keys
}

// runningVaultOutput returns safe local's output for the running vault, from
// the tee'd log when it has the seal key, and otherwise from the whole of the
// tmux pane's history.
func runningVaultOutput(ctx context.Context, paths map[string]string) string {
	logData, err := os.ReadFile(paths["logFile"]) // #nosec G304 -- the bloc's own log file from getVaultInceptionPaths()
	if err == nil {
		output := stripANSI(string(logData))
		if extractSealKey(output) != "" {
			return output
		}
	}

	pane, err := vaultOps.run(ctx, cleanupCommand{
		name: "tmux", args: capturePaneArgs(paths["tmuxSession"], "-"), tmux: true,
	})
	if err != nil {
		return ""
	}

	return stripANSI(string(pane))
}

// blocTargetToken returns the token safe holds for the bloc's own target, or
// "" unless that target exists and points at the bloc's port.
func blocTargetToken(paths map[string]string) string {
	return keyfile.TargetToken(paths["vaultName"], paths["port"])
}
