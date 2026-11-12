package commands

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/fivetwenty-io/ocfp-cli/internal/safedir"
)

// refuseDefaultReach refuses a directory flag that names the default
// directory def, or a directory under it, by any path but def as written.
// The default as written, after filepath.Clean, is always accepted, so a
// default that sits behind a link keeps working. Any other value is refused
// when it resolves to def or to a directory under it, as reaches decides.
func refuseDefaultReach(flag, value, def string) error {
	if filepath.Clean(value) == filepath.Clean(def) || !reaches(value, def) {
		return nil
	}

	return fmt.Errorf("%s %s leads to the default directory %s or to a directory under it, so pass the default as written or a directory outside it",
		flag, value, def)
}

// refuseSpoolInOwner refuses a spool root that resolves to the owner
// directory or to a directory under it, by any path, so that job output
// never lands among the owner record's files.
func refuseSpoolInOwner(spoolRoot, ownerDir string) error {
	if !reaches(spoolRoot, ownerDir) {
		return nil
	}

	return fmt.Errorf("--spool-root %s resolves into the owner directory %s", spoolRoot, ownerDir)
}

// reaches reports whether path resolves to root or to a directory under it.
// Both are resolved with safedir.Resolve. When root does not exist yet, or a
// directory above it does not, the nearest directory that does exist is
// resolved and the missing names are put back after it, so the answer holds
// before first use. The same is done for path.
//
// When either side cannot be resolved for any other reason, reaches reports
// false and leaves path to the store or the spool. Those check the same
// directories above the path and refuse it with their own error. A path that
// reached root would pass through every directory above root, so it fails
// the same check that root fails.
func reaches(path, root string) bool {
	resolvedRoot, err := resolveNearest(root)
	if err != nil {
		return false
	}

	resolved, err := resolveNearest(path)
	if err != nil {
		return false
	}

	return within(resolved, resolvedRoot)
}

// resolveNearest resolves path with safedir.Resolve. When a directory above
// path does not exist yet, it resolves the nearest one that does and puts the
// missing names back after it.
func resolveNearest(path string) (string, error) {
	dir := filepath.Clean(path)

	var missing []string

	for {
		resolved, err := safedir.Resolve(dir)
		if err == nil {
			return filepath.Join(append([]string{resolved}, missing...)...), nil
		}

		parent := filepath.Dir(dir)
		if !errors.Is(err, fs.ErrNotExist) || parent == dir {
			return "", err
		}

		missing = append([]string{filepath.Base(dir)}, missing...)
		dir = parent
	}
}

// within reports whether path is root or lies under it. Both are clean and
// absolute.
func within(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}

	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
