package commands

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

const (
	// targetTokenSidecarInfix names the copy of safe's root token that is
	// kept beside root.key when root.key holds something else:
	// root.key.saferc-<timestamp>.
	targetTokenSidecarInfix = ".saferc-"

	// rejectedTokenInfix names where a root.key the engine refused is moved
	// once safe's token has opened the vault: root.key.rejected-<timestamp>.
	rejectedTokenInfix = ".rejected-"

	// keyFileNameAttempts bounds the -N suffixes tried for a free name.
	keyFileNameAttempts = 100
)

// ErrKeyFileNameTaken reports that every name tried for a kept copy of a key
// was already taken.
var ErrKeyFileNameTaken = errors.New("no free name to keep a copy of the inception vault root token")

// preserveTargetToken makes sure the root token safe holds for the bloc's
// target is on disk before anything stops the vault. Stopping deletes that
// target, and with it what may be the only copy of the token.
//
// When root.key is missing or blank and the token has the shape of a token,
// the token is written to root.key. When root.key already holds the token,
// nothing changes. Otherwise root.key holds a different value, or the token
// is not token-shaped, and both are kept: the token goes to a copy beside
// root.key, root.key.saferc-<timestamp>, which an archive of <bloc>/vault
// carries along. An identical copy kept by an earlier run is reused rather
// than kept twice.
//
// It returns the file that now holds the token, or "" when safe has none.
// A root.key that cannot be read is an error, and nothing is written then.
func preserveTargetToken(paths map[string]string, token string, now time.Time, log *zap.SugaredLogger) (string, error) {
	if token == "" {
		return "", nil
	}

	rootKeyFile := paths["rootKeyFile"]

	held, err := readKeyFile(rootKeyFile)
	if err != nil {
		return "", err
	}

	if held == token {
		return rootKeyFile, nil
	}

	if held == "" && checkRootToken(token) == nil {
		err = writeRecoveredKey(rootKeyFile, token)
		if err != nil {
			return "", err
		}

		log.Infow("Saved the root token from safe's target before stopping the vault", "path", rootKeyFile,
			"target", paths["vaultName"])

		return rootKeyFile, nil
	}

	return keepTargetTokenCopy(paths, token, now, log)
}

// keepTargetTokenCopy keeps the target's token beside root.key, reusing a
// copy an earlier run kept when it holds the same token.
func keepTargetTokenCopy(paths map[string]string, token string, now time.Time, log *zap.SugaredLogger) (string, error) {
	rootKeyFile := paths["rootKeyFile"]

	existing, err := keptTokenCopies(rootKeyFile)
	if err != nil {
		return "", err
	}

	for _, kept := range existing {
		// A copy that cannot be read cannot be compared, so a new copy is
		// kept beside it rather than trusting it.
		value, readErr := readKeyFile(kept)
		if readErr == nil && value == token {
			return kept, nil
		}
	}

	kept, err := writeKeyFileUnderFreeName(rootKeyFile+targetTokenSidecarInfix+now.Format(vaultArchiveTimeFormat), token)
	if err != nil {
		return "", err
	}

	log.Warnw("Kept the root token from safe's target beside root.key, which holds a different value",
		"kept", kept, "root_key", rootKeyFile, "target", paths["vaultName"])

	return kept, nil
}

// keptTokenCopies lists the copies of safe's token kept beside root.key.
func keptTokenCopies(rootKeyFile string) ([]string, error) {
	dir := filepath.Dir(rootKeyFile)

	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list %s: %w", dir, err)
	}

	prefix := filepath.Base(rootKeyFile) + targetTokenSidecarInfix

	var kept []string

	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), prefix) {
			kept = append(kept, filepath.Join(dir, entry.Name()))
		}
	}

	return kept, nil
}

// writeKeyFileUnderFreeName writes value to base, or to base with the first
// free -N appended, and never replaces an existing file.
func writeKeyFileUnderFreeName(base, value string) (string, error) {
	candidate := base

	for n := 2; n <= keyFileNameAttempts; n++ {
		err := writeNewKeyFile(candidate, value)
		if err == nil {
			return candidate, nil
		}

		if !errors.Is(err, ErrVaultArchiveExists) {
			return "", err
		}

		candidate = base + "-" + strconv.Itoa(n)
	}

	return "", fmt.Errorf("%w: %s", ErrKeyFileNameTaken, base)
}

// promoteTargetToken makes root.key hold the token that just opened the
// vault, after the engine refused the one root.key held. The refused token is
// moved aside to root.key.rejected-<timestamp> first, never deleted.
func promoteTargetToken(paths map[string]string, token string, now time.Time, log *zap.SugaredLogger) error {
	rootKeyFile := paths["rootKeyFile"]

	_, err := os.Lstat(rootKeyFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("failed to inspect %s: %w", rootKeyFile, err)
	}

	var rejected string

	if err == nil {
		rejected, err = moveAsideUnderFreeName(rootKeyFile, rootKeyFile+rejectedTokenInfix+now.Format(vaultArchiveTimeFormat))
		if err != nil {
			return err
		}
	}

	err = writeRecoveredKey(rootKeyFile, token)
	if err != nil {
		return err
	}

	log.Warnw("root.key held a root token the engine refused; it now holds the token from safe's target",
		"path", rootKeyFile, "refused_token_kept", rejected)

	return nil
}

// moveAsideUnderFreeName renames path to base, or to base with the first
// free -N appended, and never replaces anything.
func moveAsideUnderFreeName(path, base string) (string, error) {
	candidate := base

	for n := 2; n <= keyFileNameAttempts; n++ {
		err := renameRefusingExisting(path, candidate)
		if err == nil {
			return candidate, nil
		}

		if !errors.Is(err, ErrVaultArchiveExists) {
			return "", err
		}

		candidate = base + "-" + strconv.Itoa(n)
	}

	return "", fmt.Errorf("%w: %s", ErrKeyFileNameTaken, base)
}
