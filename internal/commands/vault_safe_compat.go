package commands

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// MinSafeVersion is the oldest safe release that can run a raft-backed
// inception vault the way ocfp starts one: `safe local --raft` with an
// explicit --cluster-port, and --root-token-file to reopen a vault with its
// saved root token instead of calling generate-root.
const MinSafeVersion = "1.25.0"

// safeReleasesURL is where safe binaries are published for hosts without
// Homebrew, such as the bastion.
const safeReleasesURL = "https://github.com/cloudfoundry-community/safe/releases"

// safeLocalRequiredOptions are the `safe local` options ocfp passes that older
// releases do not understand. A development build is judged by whether its
// help text mentions them.
//
//nolint:gochecknoglobals // fixed list, read-only
var safeLocalRequiredOptions = []string{"--cluster-port", "--root-token-file"}

// ErrSafeTooOld reports a safe that cannot run a raft-backed inception vault.
var ErrSafeTooOld = errors.New("safe is too old for raft-backed inception vaults")

// safeVersionPattern matches the first line `safe --version` prints for a
// release build, such as "safe v1.24.0" or "safe v1.25.0-rc.1".
var safeVersionPattern = regexp.MustCompile(`^safe v(\d+)\.(\d+)\.(\d+)(\S*)`)

// checkSafeCompatibility refuses a safe older than MinSafeVersion.
//
// An older safe would reject --cluster-port and --root-token-file, and
// without the token file it falls back to generate-root to reopen a vault,
// which OpenBao no longer allows. Failing here, before any vault is stopped
// or moved, turns that into an upgrade message instead of a half-done
// restart.
//
// A release is judged by its version number. A development build, or a
// pre-release of the minimum version, is judged by whether `safe help local`
// mentions the options ocfp needs.
func checkSafeCompatibility(ctx context.Context, safePath string) error {
	out, err := exec.CommandContext(ctx, safePath, "--version").CombinedOutput() // #nosec G204 -- safePath is the resolved safe binary
	if err != nil && len(out) == 0 {
		return fmt.Errorf("failed to run %s --version: %w", safePath, err)
	}

	output := strings.TrimSpace(string(out))

	version, prerelease, ok := parseSafeVersion(output)
	if ok {
		switch cmp := compareVersions(version, minSafeVersionParts()); {
		case cmp > 0:
			return nil
		case cmp < 0:
			return safeTooOldError(fmt.Sprintf("safe v%d.%d.%d", version[0], version[1], version[2]))
		case !prerelease:
			return nil
		}
	}

	if safeHelpOffersRequiredOptions(ctx, safePath) {
		return nil
	}

	installed, _, _ := strings.Cut(output, "\n")
	if installed == "" {
		installed = "an unknown safe version"
	}

	return safeTooOldError(installed)
}

// parseSafeVersion reads the version from `safe --version` output. It
// reports whether the version carries a pre-release suffix, and ok is false
// for a development build or anything else it does not recognise.
func parseSafeVersion(output string) ([3]int, bool, bool) {
	var version [3]int

	match := safeVersionPattern.FindStringSubmatch(strings.TrimSpace(output))
	if match == nil {
		return version, false, false
	}

	for i := range version {
		n, err := strconv.Atoi(match[i+1])
		if err != nil {
			return version, false, false
		}

		version[i] = n
	}

	return version, strings.HasPrefix(match[4], "-"), true
}

// minSafeVersionParts returns MinSafeVersion as numbers.
func minSafeVersionParts() [3]int {
	version, _, _ := parseSafeVersion("safe v" + MinSafeVersion)

	return version
}

// compareVersions returns -1, 0, or 1 as a is older than, equal to, or newer
// than b.
func compareVersions(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}

			return 1
		}
	}

	return 0
}

// safeHelpOffersRequiredOptions reports whether `safe help local` mentions
// every option ocfp passes. The exit status is ignored; only the text counts.
func safeHelpOffersRequiredOptions(ctx context.Context, safePath string) bool {
	out, _ := exec.CommandContext(ctx, safePath, "help", "local").CombinedOutput() // #nosec G204 -- safePath is the resolved safe binary

	for _, option := range safeLocalRequiredOptions {
		if !strings.Contains(string(out), option) {
			return false
		}
	}

	return true
}

// safeTooOldError builds the upgrade message for an installed safe.
func safeTooOldError(installed string) error {
	return fmt.Errorf("%w: %s is installed, but ocfp needs safe v%s or later to run the inception vault "+
		"(safe local --raft, --cluster-port, and --root-token-file); upgrade with "+
		"'brew upgrade cloudfoundry-community/cf/safe', or download a release from %s",
		ErrSafeTooOld, installed, MinSafeVersion, safeReleasesURL)
}
