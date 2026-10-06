package commands

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

// vaultLogSharedBits are the group and other permission bits that the log
// directory and its logs must not carry, because a log may hold the only copy
// of an unseal key.
const vaultLogSharedBits fs.FileMode = 0o077

// chmodVaultLog changes the mode of the log directory or a log. Tests replace
// it to make a change fail.
var chmodVaultLog = os.Chmod //nolint:gochecknoglobals // test seam for a failed chmod

// olderVaultLogInfix names the logs of the starts before the previous one.
// setAsidePreviousVaultLog keeps each as <log>.previous-<timestamp>, with -N
// appended when that name is taken.
const olderVaultLogInfix = previousVaultLogSuffix + "-"

// olderVaultLog is one log kept as <log>.previous-<timestamp>[-N].
type olderVaultLog struct {
	path  string
	stamp string
	n     int
}

// vaultLogFiles lists every log the bloc's vault starts left behind, newest
// first: the log, the log of the start before it, and then each older log
// that setAsidePreviousVaultLog kept. The first two are listed whether or not
// they exist. The older ones are found by listing the log directory, and a
// directory that cannot be listed is an error, because a log in it may hold
// the only copy of an unseal key.
func vaultLogFiles(paths map[string]string) ([]string, error) {
	logFile := paths["logFile"]

	older, err := olderVaultLogs(logFile)
	if err != nil {
		return nil, err
	}

	return append([]string{logFile, logFile + previousVaultLogSuffix}, older...), nil
}

// olderVaultLogs lists the logs kept as <log>.previous-<timestamp>[-N],
// newest first. Names are ordered by their timestamp and then by their -N
// suffix as a number, which is the order setAsidePreviousVaultLog made them
// in. Anything else in the directory, including a directory with such a
// name, is not a kept log and is left out.
func olderVaultLogs(logFile string) ([]string, error) {
	dir := filepath.Dir(logFile)

	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list the inception vault logs in %s: %w", dir, err)
	}

	prefix := filepath.Base(logFile) + olderVaultLogInfix

	var logs []olderVaultLog

	for _, entry := range entries {
		rest, ok := strings.CutPrefix(entry.Name(), prefix)
		if !ok || !entry.Type().IsRegular() {
			continue
		}

		stamp, n, ok := parseOlderVaultLogSuffix(rest)
		if ok {
			logs = append(logs, olderVaultLog{path: filepath.Join(dir, entry.Name()), stamp: stamp, n: n})
		}
	}

	slices.SortFunc(logs, func(a, b olderVaultLog) int {
		return cmp.Or(cmp.Compare(b.stamp, a.stamp), cmp.Compare(b.n, a.n))
	})

	files := make([]string, 0, len(logs))
	for _, kept := range logs {
		files = append(files, kept.path)
	}

	return files, nil
}

// parseOlderVaultLogSuffix splits what follows <log>.previous- into its
// timestamp and its -N suffix, where a name without one counts as 1. The
// timestamp format sorts in time order as a string. It reports false for
// anything setAsidePreviousVaultLog does not write.
func parseOlderVaultLogSuffix(rest string) (string, int, bool) {
	width := len(vaultArchiveTimeFormat)
	if len(rest) < width {
		return "", 0, false
	}

	stamp := rest[:width]

	_, err := time.Parse(vaultArchiveTimeFormat, stamp)
	if err != nil {
		return "", 0, false
	}

	if len(rest) == width {
		return stamp, 1, true
	}

	digits, ok := strings.CutPrefix(rest[width:], "-")
	if !ok || digits == "" || strings.Trim(digits, "0123456789") != "" {
		return "", 0, false
	}

	n, err := strconv.Atoi(digits)
	if err != nil || n < 2 {
		return "", 0, false
	}

	return stamp, n, true
}

// vaultLogHint names the vault logs that exist, newest first, for an error
// that sends the operator to them for the keys. It never reads a log.
func vaultLogHint(paths map[string]string) string {
	files, err := vaultLogFiles(paths)
	if err != nil {
		return fmt.Sprintf("its full keys may be in the vault log at %s or in an older log beside it (%v)",
			paths["logFile"], err)
	}

	var existing []string

	for _, logFile := range files {
		_, statErr := os.Lstat(logFile)
		if statErr == nil {
			existing = append(existing, logFile)
		}
	}

	switch len(existing) {
	case 0:
		return "no vault log was found at " + paths["logFile"] + ", so its keys must come from a copy kept elsewhere"
	case 1:
		return "its full keys may be in the vault log at " + existing[0]
	default:
		return "its full keys may be in one of the vault logs, newest first: " + strings.Join(existing, ", ")
	}
}

// makeVaultLogDir creates the log directory private to the user, then removes
// group and other access from it and from the logs already in it.
func makeVaultLogDir(paths map[string]string, log *zap.SugaredLogger) error {
	err := os.MkdirAll(paths["logDir"], VaultDirMode)
	if err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	tightenVaultLogs(paths, log)

	return nil
}

// tightenVaultLogs removes group and other access from the log directory and
// from each log in it: the log, the previous start's log, and the older logs.
// Older releases created them readable by other users. It changes modes only,
// never opens or reads a log, and leaves alone anything in the directory that
// is not a regular file named like a log, so a link is never followed. A
// missing directory is left missing. A mode that cannot be read or changed is
// logged as a warning rather than stopping the run, because the logs are
// still where the vault's keys can be recovered from.
func tightenVaultLogs(paths map[string]string, log *zap.SugaredLogger) {
	dir := paths["logDir"]

	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}

	if err != nil {
		log.Warnw("Could not check that the inception vault log directory is private", "path", dir, "error", err)

		return
	}

	if !info.IsDir() {
		return
	}

	tightenVaultLogMode(dir, info.Mode(), log)

	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Warnw("Could not list the inception vault logs to make them private", "path", dir, "error", err)

		return
	}

	prefix := filepath.Base(paths["logFile"])

	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) || !entry.Type().IsRegular() {
			continue
		}

		path := filepath.Join(dir, entry.Name())

		// Info reports on the entry itself, as Lstat does, so a log swapped
		// for a link since the listing is seen as one and skipped.
		entryInfo, infoErr := entry.Info()
		if errors.Is(infoErr, fs.ErrNotExist) {
			continue
		}

		if infoErr != nil {
			log.Warnw("Could not check that an inception vault log is private", "path", path, "error", infoErr)

			continue
		}

		if entryInfo.Mode().IsRegular() {
			tightenVaultLogMode(path, entryInfo.Mode(), log)
		}
	}
}

// tightenVaultLogMode removes group and other access from path, which has
// mode, and keeps the owner's access as it is.
func tightenVaultLogMode(path string, mode fs.FileMode, log *zap.SugaredLogger) {
	perm := mode.Perm()
	if perm&vaultLogSharedBits == 0 {
		return
	}

	err := chmodVaultLog(path, perm&^vaultLogSharedBits)
	if err != nil {
		log.Warnw("Could not make an inception vault log private, so other users may still read it",
			"path", path, "error", err)

		return
	}

	log.Infow("Removed group and other access from an inception vault log", "path", path)
}
