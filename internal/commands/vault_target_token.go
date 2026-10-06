package commands

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/keyfile"
	"go.uber.org/zap"
)

const (
	// rejectedTokenInfix names where a root.key the engine refused is moved
	// once safe's token has opened the vault: root.key.rejected-<timestamp>.
	rejectedTokenInfix = ".rejected-"
)

// ErrNoFreeKeepName reports that every name tried for keeping a copy of a
// root token, or an older vault log, was already taken.
var ErrNoFreeKeepName = keyfile.ErrNoFreeKeepName

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

	err = keyfile.WriteRecovered(rootKeyFile, token)
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

	for n := 2; n <= keyfile.KeepNameAttempts; n++ {
		err := renameRefusingExisting(path, candidate)
		if err == nil {
			return candidate, nil
		}

		if !errors.Is(err, ErrVaultArchiveExists) {
			return "", err
		}

		candidate = base + "-" + strconv.Itoa(n)
	}

	return "", fmt.Errorf("%w: %s", ErrNoFreeKeepName, base)
}
