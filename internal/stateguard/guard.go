// Package stateguard fails a test run that changes the developer's own
// ocfp directories.
//
// It compares what is in the directories before and after the run by name,
// size, mode, and modification time. It never opens a file, so it reads no
// secrets, and its report names paths and digests only.
//
// A test that resolves a path from the passwd entry, or that forgets to
// point HOME and the XDG variables at a temporary directory, writes bloc
// state under the developer's real ~/.local/state/ocfp. A package whose
// tests reach code that creates bloc state calls Run from its TestMain.
package stateguard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"sort"
)

// Run runs the package's tests through m and returns their exit code. If
// anything under the developer's ocfp directories changed while they ran,
// it names the paths on stderr and the run fails.
func Run(m interface{ Run() int }) int {
	account, err := user.Current()
	if err != nil || account.HomeDir == "" {
		return m.Run()
	}

	override := map[string]string{
		"XDG_STATE_HOME":  os.Getenv("XDG_STATE_HOME"),
		"XDG_DATA_HOME":   os.Getenv("XDG_DATA_HOME"),
		"XDG_CONFIG_HOME": os.Getenv("XDG_CONFIG_HOME"),
		"OCFP_HOME":       os.Getenv("OCFP_HOME"),
	}

	return guard(roots(account.HomeDir, override), os.Stderr, m.Run)
}

// roots lists the directories ocfp keeps its files in: the defaults under
// the passwd home, and any directory an environment variable moved them
// to when the run started.
func roots(home string, override map[string]string) []string {
	list := []string{
		filepath.Join(home, ".local", "state", "ocfp"),
		filepath.Join(home, ".local", "share", "ocfp"),
		filepath.Join(home, ".config", "ocfp"),
		filepath.Join(home, ".ocfp"),
	}

	for _, key := range []string{"XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME"} {
		if base := override[key]; base != "" {
			list = append(list, filepath.Join(base, "ocfp"))
		}
	}

	if dir := override["OCFP_HOME"]; dir != "" {
		list = append(list, dir)
	}

	seen := map[string]bool{}
	unique := list[:0]

	for _, root := range list {
		if !seen[root] {
			seen[root] = true
			unique = append(unique, root)
		}
	}

	return unique
}

// guard runs run and returns its code, or a failing code when anything
// under the roots differs afterwards, reporting each changed path and the
// digests of its entry before and after to report.
func guard(roots []string, report io.Writer, run func() int) int {
	before := make([]map[string]string, len(roots))
	for i, root := range roots {
		before[i] = snapshot(root)
	}

	code := run()

	for i, root := range roots {
		if !compare(root, before[i], snapshot(root), report) {
			continue
		}

		if code == 0 {
			code = 1
		}
	}

	return code
}

// compare reports each path whose entry differs between before and after,
// and says whether any did.
func compare(root string, before, after map[string]string, report io.Writer) bool {
	var paths []string

	for path := range before {
		paths = append(paths, path)
	}

	for path := range after {
		if _, ok := before[path]; !ok {
			paths = append(paths, path)
		}
	}

	sort.Strings(paths)

	changed := false

	for _, path := range paths {
		was, hadBefore := before[path]
		now, hasAfter := after[path]

		switch {
		case !hadBefore:
			_, _ = fmt.Fprintf(report, "FAIL: the tests added %s (%s)\n", path, now)
		case !hasAfter:
			_, _ = fmt.Fprintf(report, "FAIL: the tests removed %s (was %s)\n", path, was)
		case was != now:
			_, _ = fmt.Fprintf(report, "FAIL: the tests changed %s (was %s, now %s)\n", path, was, now)
		default:
			continue
		}

		changed = true
	}

	if changed {
		_, _ = fmt.Fprintf(report, "A test wrote under %s. It needs t.Setenv for HOME and the XDG variables, "+
			"and a t.TempDir() to point them at.\n", root)
	}

	return changed
}

// snapshot returns one entry for every path under root, keyed by path. A
// file's entry is a SHA-256 digest of its size, mode, and modification time
// to the nanosecond; a directory's or a link's entry names its kind. It
// never opens a file, so it reads no contents and takes no time on the
// large vaults and caches that live under these directories. It does not
// follow links, and a root that does not exist yields no entries. A path
// that cannot be read gets an entry naming the error, so a change in what
// can be read still shows.
func snapshot(root string) map[string]string {
	entries := map[string]string{}

	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			entries[path] = "unreadable: " + err.Error()

			return nil //nolint:nilerr // an unreadable path is an entry, and the walk goes on
		}

		entries[path] = describe(path, entry)

		return nil
	})

	return entries
}

// describe returns the entry for one path.
func describe(path string, entry fs.DirEntry) string {
	info, err := entry.Info()
	if err != nil {
		return "unreadable: " + err.Error()
	}

	switch {
	case info.IsDir():
		return "dir"
	case info.Mode()&fs.ModeSymlink != 0:
		target, linkErr := os.Readlink(path)
		if linkErr != nil {
			return "link unreadable: " + linkErr.Error()
		}

		return "link to " + target
	case !info.Mode().IsRegular():
		return "special " + info.Mode().String()
	}

	sum := sha256.Sum256([]byte(fmt.Sprintf("%d %s %d", info.Size(), info.Mode(), info.ModTime().UnixNano())))

	return "file " + hex.EncodeToString(sum[:])
}
