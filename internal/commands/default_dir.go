package commands

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/fivetwenty-io/ocfp-cli/internal/safedir"
)

// refuseDefaultReach refuses a directory flag that names the default
// directory def, or a directory under it, by any path but def as written.
// The default as written, after filepath.Clean, is always accepted, so a
// default that sits behind a link keeps working. Any other value is refused
// when it is def or lies under it, as inside decides.
//
// When def fails its own check, every value but def as written is refused,
// because there is then no safe way to tell where def is.
func refuseDefaultReach(flag, value, def string) error {
	if filepath.Clean(value) == filepath.Clean(def) {
		return nil
	}

	d, err := locateChecked(def)
	if err != nil {
		return fmt.Errorf("the default directory %s cannot be checked, so %s %s is refused: %w", def, flag, value, err)
	}

	v, err := locate(value)
	if err != nil {
		return fmt.Errorf("%s %s cannot be checked: %w", flag, value, err)
	}

	in, err := inside(v, d)
	if err != nil {
		return fmt.Errorf("%s %s cannot be checked: %w", flag, value, err)
	}

	if in {
		return fmt.Errorf("%s %s leads to the default directory %s or to a directory under it, so pass the default as written or a directory outside it",
			flag, value, def)
	}

	return nil
}

// refuseSpoolOverlap refuses a spool root that shares a tree with the owner
// directory in use, ownerDir, or with the default owner directory def. A
// spool root is refused when it is either directory, lies inside it, or
// holds it, so that job output never lands among an owner record's files
// and a job's spool never takes the place of an owner directory.
//
// The owner directory in use is located without the safedir check, because
// the owner store makes that check itself and refuses an unsafe directory,
// with its own code, before the job makes anything under the spool root.
// When def is not the owner directory in use and fails its own check, the
// spool root is refused, because there is then no safe way to tell where def
// is.
func refuseSpoolOverlap(spoolRoot, ownerDir, def string) error {
	owner, err := locate(ownerDir)
	if err != nil {
		return fmt.Errorf("the owner directory %s cannot be checked, so --spool-root %s is refused: %w", ownerDir, spoolRoot, err)
	}

	d := owner

	if filepath.Clean(def) != filepath.Clean(ownerDir) {
		d, err = locateChecked(def)
		if err != nil {
			return fmt.Errorf("the default owner directory %s cannot be checked, so --spool-root %s is refused: %w", def, spoolRoot, err)
		}
	}

	spool, err := locate(spoolRoot)
	if err != nil {
		return fmt.Errorf("--spool-root %s cannot be checked: %w", spoolRoot, err)
	}

	checks := []struct {
		inner, outer located
		msg          string
		dir          string
	}{
		{spool, owner, "--spool-root %s resolves into the owner directory %s", ownerDir},
		{owner, spool, "--spool-root %s holds the owner directory %s", ownerDir},
		{spool, d, "--spool-root %s resolves into the default owner directory %s", def},
		{d, spool, "--spool-root %s holds the default owner directory %s", def},
	}

	for _, c := range checks {
		in, err := inside(c.inner, c.outer)
		if err != nil {
			return fmt.Errorf("--spool-root %s cannot be checked: %w", spoolRoot, err)
		}

		if in {
			return fmt.Errorf(c.msg, spoolRoot, c.dir)
		}
	}

	return nil
}

// located is a directory found on disk. near is the nearest directory on its
// path that exists, with every symbolic link resolved, and missing holds the
// names below near that do not exist yet.
type located struct {
	near    string
	missing []string
}

// locate finds path on disk. When path, or a directory above it, does not
// exist yet, locate finds the nearest directory that does and keeps the
// missing names. A name that exists as a symbolic link to something that does
// not exist yet is refused, because where it will lead cannot be known.
func locate(path string) (located, error) {
	l, _, err := locateFrom(path)

	return l, err
}

// locateChecked locates path as locate does and also checks every directory
// above the nearest existing one with safedir, refusing what safedir refuses.
func locateChecked(path string) (located, error) {
	l, at, err := locateFrom(path)
	if err != nil {
		return located{}, err
	}

	if _, err := safedir.Resolve(at); err != nil {
		return located{}, err
	}

	return l, nil
}

// locateFrom locates path as locate describes and also returns the nearest
// existing directory as path spells it.
func locateFrom(path string) (located, string, error) {
	dir := filepath.Clean(path)

	var missing []string

	for {
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			return located{near: resolved, missing: missing}, dir, nil
		}

		if !errors.Is(err, fs.ErrNotExist) {
			return located{}, "", err
		}

		if _, lerr := os.Lstat(dir); lerr == nil {
			return located{}, "", fmt.Errorf("%s is a symbolic link to a directory that does not exist yet", dir)
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return located{}, "", err
		}

		missing = append([]string{filepath.Base(dir)}, missing...)
		dir = parent
	}
}

// inside reports whether a is the directory b or lies under it. It compares
// directories by identity with os.SameFile, not by name, so any second name
// for b, such as a different case, a firmlink, or a bind mount, counts as b.
// It walks up from the nearest existing directory of a. When it meets the
// nearest existing directory of b, a is inside b if the names that follow
// start with the names b is still missing. Those names are compared without
// regard to case or Unicode normalization, so that a doubtful case is
// counted as inside.
func inside(a, b located) (bool, error) {
	target, err := os.Stat(b.near)
	if err != nil {
		return false, err
	}

	dir := a.near
	trail := a.missing

	for {
		info, err := os.Stat(dir)
		if err != nil {
			return false, err
		}

		if os.SameFile(info, target) {
			return namesStartWith(trail, b.missing), nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return false, nil
		}

		trail = append([]string{filepath.Base(dir)}, trail...)
		dir = parent
	}
}

// namesStartWith reports whether names begins with prefix, comparing each
// name without regard to case or Unicode normalization.
func namesStartWith(names, prefix []string) bool {
	if len(prefix) > len(names) {
		return false
	}

	for i, p := range prefix {
		if !strings.EqualFold(norm.NFC.String(names[i]), norm.NFC.String(p)) {
			return false
		}
	}

	return true
}
