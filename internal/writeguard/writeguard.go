// Package writeguard stops a test process from writing under the
// developer's real ocfp directories.
//
// Every function that writes ocfp config, state, data, keys, locks, or logs
// calls Check with the path it is about to write. In a real run Check
// returns at once, because only a test process sets EnvVar. In a test
// process it panics, with the stack of the write, when the path lies under
// one of the developer's real ocfp directories.
//
// The real directories come from the passwd entry, not from $HOME or the
// XDG variables, because a test process points those at a temporary tree.
// A guard that read them would see that tree as the real home and stop
// every legitimate write.
package writeguard

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// EnvVar marks a test process. A test binary's TestMain sets it, and a real
// run never does.
const EnvVar = "OCFP_TEST_SAFETY_GUARD"

// RootsEnvVar lists more directories to guard, separated by
// os.PathListSeparator. A test binary's TestMain sets it to the ocfp
// directories that HOME, the XDG variables, and OCFP_HOME named before it
// moved them. A developer whose real directories aren't the defaults keeps
// them guarded that way, and so does a process with no passwd entry.
const RootsEnvVar = "OCFP_TEST_SAFETY_GUARD_ROOTS"

// appDirName is the directory ocfp keeps inside each XDG base directory.
const appDirName = "ocfp"

// passwdHome looks up the home directory in the passwd entry once per
// process. The entry can't change while a process runs.
//
//nolint:gochecknoglobals // a process-wide cache of the passwd lookup
var passwdHome = sync.OnceValues(func() (string, error) {
	account, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("look up the current user: %w", err)
	}

	if account.HomeDir == "" {
		return "", fmt.Errorf("the passwd entry for %s has no home directory", account.Username)
	}

	return account.HomeDir, nil
})

// homeOverride replaces the passwd home in this package's own tests and in
// tests that prove a caller is guarded. It is nil otherwise.
//
//nolint:gochecknoglobals // a test seam, read atomically by every Check
var homeOverride atomic.Pointer[string]

// OverrideRealHome makes Check treat dir as the developer's real home until
// the returned function runs. A test uses it to prove that a write under
// the real directories panics, without writing under them. It must not run
// in parallel with another test that calls it.
func OverrideRealHome(dir string) func() {
	previous := homeOverride.Swap(&dir)

	return func() { homeOverride.Store(previous) }
}

// Check panics when this is a test process and path lies under one of the
// developer's real ocfp directories. operation names the write in the panic
// message. An empty path passes, since it names no directory.
//
// When this process has no passwd home and RootsEnvVar is empty, Check
// panics too. A test process that can't tell where the real directories
// are can't prove that it stays out of them.
func Check(operation, path string) {
	if os.Getenv(EnvVar) == "" || path == "" {
		return
	}

	roots, err := Roots()
	if err != nil {
		panic(fmt.Sprintf("SAFETY GUARD: %s cannot check %s against the real ocfp directories: %v\n%s",
			operation, path, err, debug.Stack()))
	}

	target := absolute(path)
	targets := []string{target, resolveExisting(target)}

	for _, root := range roots {
		if slices.ContainsFunc(targets, func(t string) bool { return within(t, root) }) {
			panic(fmt.Sprintf("SAFETY GUARD: %s would write %s, which is under the real ocfp directory %s, "+
				"in a test process. Point HOME, the XDG variables, and OCFP_HOME at a temporary directory.\n%s",
				operation, path, root, debug.Stack()))
		}
	}
}

// Roots returns the developer's real ocfp directories. They are the
// legacy ~/.ocfp and the XDG config, state, data, and cache directories
// under the passwd home, followed by any that RootsEnvVar lists. A root
// that a symbolic link leads to appears twice, once by each name. Without
// a passwd home, Roots returns those that RootsEnvVar lists, and it fails
// when that list is empty too.
func Roots() ([]string, error) {
	var roots []string

	home, homeErr := realHome()
	if homeErr == nil {
		roots = append(roots,
			filepath.Join(home, ".ocfp"),
			filepath.Join(home, ".config", appDirName),
			filepath.Join(home, ".local", "state", appDirName),
			filepath.Join(home, ".local", "share", appDirName),
			filepath.Join(home, ".cache", appDirName),
		)
	}

	for _, extra := range filepath.SplitList(os.Getenv(RootsEnvVar)) {
		if extra != "" {
			roots = append(roots, absolute(extra))
		}
	}

	if len(roots) == 0 {
		return nil, homeErr
	}

	linked := make([]string, 0, len(roots))

	for _, root := range roots {
		if target, err := filepath.EvalSymlinks(root); err == nil && target != root {
			linked = append(linked, target)
		}
	}

	return append(roots, linked...), nil
}

// realHome returns the override when a test set one, else the passwd home.
func realHome() (string, error) {
	if dir := homeOverride.Load(); dir != nil {
		return *dir, nil
	}

	return passwdHome()
}

// absolute returns path made absolute and clean. A path that can't be made
// absolute is only cleaned, so it is compared as it stands.
func absolute(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}

	return abs
}

// resolveExisting returns path with the symbolic links in its longest
// existing prefix resolved, so a write through a link is compared by where
// it lands. The part of path that doesn't exist yet is kept as it stands.
func resolveExisting(path string) string {
	rest := ""

	for dir := path; ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}

		if filepath.Dir(dir) == dir {
			return path
		}

		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

// within reports whether target is root or lies below it. Both are clean
// absolute paths.
func within(target, root string) bool {
	if target == root {
		return true
	}

	return strings.HasPrefix(target, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}
