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
// explicit --cluster-port, --root-token-file to reopen a vault with its
// saved root token instead of calling generate-root, and --keep-current on
// `safe local` and `safe target` so that registering the bloc's target
// never changes the operator's current target.
const MinSafeVersion = "1.27.0"

// safeReleasesURL is where safe binaries are published for hosts without
// Homebrew, such as the bastion.
const safeReleasesURL = "https://github.com/cloudfoundry-community/safe/releases"

// safeLocalRequiredOptions are the `safe local` options ocfp passes that older
// releases do not understand. A development build is judged by whether its
// help text mentions them.
//
//nolint:gochecknoglobals // fixed list, read-only
var safeLocalRequiredOptions = []string{"--cluster-port", "--root-token-file", safeKeepCurrentOption}

// ErrSafeTooOld reports a safe that cannot run a raft-backed inception vault.
var ErrSafeTooOld = errors.New("safe is too old for raft-backed inception vaults")

// ErrSafeHelpUnreadable reports a `safe help` that printed nothing or could
// not run, which says nothing about the options safe offers.
var ErrSafeHelpUnreadable = errors.New("could not read safe's help")

// ErrSafeLacksKeepCurrent reports a safe that cannot register a target
// without making it the current one.
var ErrSafeLacksKeepCurrent = errors.New("safe cannot register a target without selecting it")

// safeKeepCurrentOption is the option that registers a target, with
// `safe target <name> <url>` or `safe local`, and leaves the operator's
// current target as it was.
const safeKeepCurrentOption = "--keep-current"

// safeKeepCurrentCommands are the safe commands ocfp registers a target
// with, each of which must offer safeKeepCurrentOption.
//
//nolint:gochecknoglobals // fixed list, read-only
var safeKeepCurrentCommands = []string{"target", "local"}

// safeVersionPattern matches the first line `safe --version` prints for a
// release build, such as "safe v1.24.0" or "safe v1.25.0-rc.1".
var safeVersionPattern = regexp.MustCompile(`^safe v(\d+)\.(\d+)\.(\d+)(\S*)`)

// checkSafeCompatibility refuses a safe older than MinSafeVersion.
//
// An older safe would reject --cluster-port, --root-token-file, or
// --keep-current. Without the token file it falls back to generate-root to
// reopen a vault, which OpenBao no longer allows, and without --keep-current
// it would select the bloc's target. Failing here, before any vault is stopped
// or moved, turns that into an upgrade message instead of a half-done
// restart.
//
// A release is judged by its version number. A development build, or a
// pre-release of the minimum version, is judged by whether `safe help local`
// mentions the options ocfp needs.
func checkSafeCompatibility(ctx context.Context, safePath string) error {
	return checkSafeCompatibilityWith(ctx, newSafeHelp(safePath))
}

// checkSafeCompatibilityWith is checkSafeCompatibility reading safe's help
// through help, which the prerequisite check shares between its checks.
func checkSafeCompatibilityWith(ctx context.Context, help *safeHelp) error {
	safePath := help.safePath

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

	offers, err := safeHelpOffersRequiredOptions(ctx, help)
	if err != nil {
		return err
	}

	if offers {
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

// safeHelp reads `safe help <command>` for one safe binary, running safe at
// most once for each command, so the checks that judge safe by its help
// share what it printed.
type safeHelp struct {
	safePath string
	pages    map[string]safeHelpPage
}

// safeHelpPage is what one `safe help <command>` printed, or why it could
// not be read.
type safeHelpPage struct {
	text string
	err  error
}

// newSafeHelp returns a safeHelp for the safe at safePath.
func newSafeHelp(safePath string) *safeHelp {
	return &safeHelp{safePath: safePath, pages: map[string]safeHelpPage{}}
}

// page returns what `safe help <command>` printed. A non-zero exit is
// ignored when safe printed its help, because only the text counts. When
// safe printed nothing, could not start, or was killed, the page says
// nothing about safe's options, and page returns ErrSafeHelpUnreadable so
// the caller does not mistake it for an old safe.
func (h *safeHelp) page(ctx context.Context, command string) (string, error) {
	if cached, ok := h.pages[command]; ok {
		return cached.text, cached.err
	}

	out, runErr := exec.CommandContext(ctx, h.safePath, "help", command).CombinedOutput() // #nosec G204 -- safePath is the resolved safe binary

	page := safeHelpPage{text: string(out)}

	var exitErr *exec.ExitError

	switch {
	case runErr != nil && !errors.As(runErr, &exitErr):
		page.err = fmt.Errorf("%w: could not run `safe help %s`: %w", ErrSafeHelpUnreadable, command, runErr)
	case exitErr != nil && exitErr.ExitCode() < 0:
		page.err = fmt.Errorf("%w: could not run `safe help %s`, which was killed: %w", ErrSafeHelpUnreadable, command, runErr)
	case strings.TrimSpace(page.text) == "":
		page.err = fmt.Errorf("%w: could not run `safe help %s`, which printed nothing; check that %s is safe",
			ErrSafeHelpUnreadable, command, h.safePath)
	}

	h.pages[command] = page

	return page.text, page.err
}

// safeHelpOffersRequiredOptions reports whether `safe help local` mentions
// every option ocfp passes, or why the help could not be read.
func safeHelpOffersRequiredOptions(ctx context.Context, help *safeHelp) (bool, error) {
	text, err := help.page(ctx, "local")
	if err != nil {
		return false, err
	}

	for _, option := range safeLocalRequiredOptions {
		if !strings.Contains(text, option) {
			return false, nil
		}
	}

	return true, nil
}

// checkSafeKeepCurrent refuses a safe whose `target` and `local` commands
// do not both offer --keep-current.
//
// The current target in ~/.saferc belongs to the operator, and other tools
// on the machine act on it. ocfp names its own target on every call that
// reaches a vault, so it never needs the current one, and it registers the
// bloc's target with --keep-current so the operator's choice stays as it
// was. A safe without the option would select the bloc's target instead, so
// the check runs before anything that could change a target. Support is read
// from safe's help, not from its version, so a development build passes when
// it has the option.
func checkSafeKeepCurrent(ctx context.Context, safePath string) error {
	return checkSafeKeepCurrentWith(ctx, newSafeHelp(safePath))
}

// checkSafeKeepCurrentWith is checkSafeKeepCurrent reading safe's help
// through help, which the prerequisite check shares between its checks.
func checkSafeKeepCurrentWith(ctx context.Context, help *safeHelp) error {
	for _, command := range safeKeepCurrentCommands {
		text, err := help.page(ctx, command)
		if err != nil {
			return err
		}

		if !strings.Contains(text, safeKeepCurrentOption) {
			return fmt.Errorf("%w: `safe help %s` does not offer %s, which ocfp needs so that it never changes "+
				"the current safe target; upgrade safe to a release with %s with "+
				"'brew upgrade cloudfoundry-community/cf/safe', or download one from %s",
				ErrSafeLacksKeepCurrent, command, safeKeepCurrentOption, safeKeepCurrentOption, safeReleasesURL)
		}
	}

	return nil
}

// safeTooOldError builds the upgrade message for an installed safe.
func safeTooOldError(installed string) error {
	return fmt.Errorf("%w: %s is installed, but ocfp needs safe v%s or later to run the inception vault "+
		"(safe local --raft, --cluster-port, --root-token-file, and --keep-current); upgrade with "+
		"'brew upgrade cloudfoundry-community/cf/safe', or download a release from %s",
		ErrSafeTooOld, installed, MinSafeVersion, safeReleasesURL)
}
