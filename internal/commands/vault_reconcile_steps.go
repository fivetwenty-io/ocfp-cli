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

	"github.com/goccy/go-yaml"
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

		keys, keysErr := inceptionKeysUsable(paths)
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

			err = writeRecoveredKey(paths["unsealKeysFile"], sealKey)
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

		err := checkRootToken(token)
		if err != nil {
			return fmt.Errorf("refusing to recover the root token of target %s into %s: %w",
				paths["vaultName"], paths["rootKeyFile"], err)
		}

		err = writeRecoveredKey(paths["rootKeyFile"], token)
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
// error.
func repairUnsealKeyFile(ctx context.Context, paths map[string]string, log *zap.SugaredLogger) error {
	keyFile := paths["unsealKeysFile"]
	if paths["rootKeyFile"] == keyFile {
		return nil
	}

	data, err := readKeyFileBytes(keyFile)
	if err != nil {
		return err
	}

	held := strings.TrimSpace(string(data))
	if held == "" {
		return nil
	}

	shapeErr := checkSealKey(held)
	if shapeErr == nil {
		return nil
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

	err = writeRecoveredKey(keyFile, found[0])
	if err != nil {
		return err
	}

	log.Warnw("Restored the whole unseal key to a key file that held only part of it", "path", keyFile)

	return nil
}

// unsealKeySources returns what safe printed for the bloc's vault, from the
// tee'd log, the previous run's log, and the tmux pane's whole history.
func unsealKeySources(ctx context.Context, paths map[string]string) []string {
	var sources []string

	for _, logFile := range []string{paths["logFile"], paths["logFile"] + ".previous"} {
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
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	data, err := os.ReadFile(filepath.Join(home, ".saferc")) // #nosec G304 -- safe's own config under the user's home
	if err != nil {
		return ""
	}

	var safeRC struct {
		Vaults map[string]struct {
			URL   string `yaml:"url"`
			Token string `yaml:"token"`
		} `yaml:"vaults"`
	}

	if yaml.Unmarshal(data, &safeRC) != nil {
		return ""
	}

	target, ok := safeRC.Vaults[paths["vaultName"]]
	if !ok || strings.TrimRight(target.URL, "/") != "http://127.0.0.1:"+paths["port"] {
		return ""
	}

	return strings.TrimSpace(target.Token)
}

// writeRecoveredKey writes one key file with mode 0600, through a temporary
// file and a rename so a crash never leaves a partial key behind.
func writeRecoveredKey(path, value string) error {
	dir := filepath.Dir(path)

	err := os.MkdirAll(dir, vaultKeyDirMode)
	if err != nil {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}

	tmpName := tmp.Name()

	_, err = tmp.WriteString(value + "\n")
	closeErr := tmp.Close()

	if err == nil {
		err = closeErr
	}

	if err == nil {
		err = os.Chmod(tmpName, VaultOutputFileMode)
	}

	if err == nil {
		err = os.Rename(tmpName, path)
	}

	if err != nil {
		_ = os.Remove(tmpName) // the temporary file holds only this write's copy of the key

		return fmt.Errorf("failed to write %s: %w", path, err)
	}

	return nil
}
