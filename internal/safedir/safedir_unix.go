//go:build unix

package safedir

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// maxLinks is how many symbolic links Ancestors follows before it gives up,
// as the kernel does for a path with too many links.
const maxLinks = 40

// Ancestors checks every directory above path as this process's effective
// user. See AncestorsAs.
func Ancestors(path string) error {
	return AncestorsAs(path, os.Geteuid())
}

// AncestorsAs checks that no user but euid and root can replace path by way
// of the directories above it. It cleans path, then walks its parent from /
// one name at a time, following symbolic links as the kernel would. Each
// directory it passes must be owned by euid or by root, and when group or
// other can write it, it must be sticky, so that only an entry's owner can
// rename or remove that entry. Each symbolic link it follows must be owned by
// euid or by root, because a link's owner can replace it in a sticky
// directory. A parent that does not exist gives an error that wraps
// fs.ErrNotExist. AncestorsAs never looks at path itself.
func AncestorsAs(path string, euid int) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	cur := string(filepath.Separator)

	info, err := os.Lstat(cur)
	if err != nil {
		return fmt.Errorf("check parents of %s: %w", path, err)
	}

	if err := checkParent(info, cur, euid); err != nil {
		return err
	}

	pending := splitPath(filepath.Dir(abs))
	links := 0

	for len(pending) > 0 {
		name := pending[0]
		pending = pending[1:]

		switch name {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)

			continue
		}

		next := filepath.Join(cur, name)

		info, err := os.Lstat(next)
		if err != nil {
			return fmt.Errorf("check parents of %s: %w", path, err)
		}

		if info.Mode()&fs.ModeSymlink != 0 {
			links++
			if links > maxLinks {
				return fmt.Errorf("parents of %s pass through more than %d symbolic links", path, maxLinks)
			}

			if err := checkOwner(info, "symbolic link", next, euid); err != nil {
				return err
			}

			target, err := os.Readlink(next)
			if err != nil {
				return fmt.Errorf("check parents of %s: %w", path, err)
			}

			if filepath.IsAbs(target) {
				cur = string(filepath.Separator)
			}

			pending = append(splitPath(target), pending...)

			continue
		}

		if !info.IsDir() {
			return fmt.Errorf("parent %s of %s is not a directory", next, path)
		}

		if err := checkParent(info, next, euid); err != nil {
			return err
		}

		cur = next
	}

	return nil
}

// Entry checks path itself as this process's effective user. See EntryAs.
func Entry(path string, dir bool) error {
	return EntryAs(path, dir, os.Geteuid())
}

// EntryAs checks that path is not a symbolic link, is a directory when dir is
// true and a regular file when it is false, is owned by euid or by root, and
// is not writable by group or other, sticky or not. A path that does not
// exist gives an error that wraps fs.ErrNotExist. EntryAs does not check the
// directories above path, which is AncestorsAs's job.
func EntryAs(path string, dir bool, euid int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("check %s: %w", path, err)
	}

	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%s is a symbolic link", path)
	case dir && !info.IsDir():
		return fmt.Errorf("%s is not a directory", path)
	case !dir && !info.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file", path)
	}

	if err := checkOwner(info, "", path, euid); err != nil {
		return err
	}

	if perm := info.Mode().Perm(); perm&0o022 != 0 {
		return fmt.Errorf("%s has mode %04o and is writable by group or other", path, perm)
	}

	return nil
}

// checkParent checks one directory above the path.
func checkParent(info fs.FileInfo, path string, euid int) error {
	if err := checkOwner(info, "parent", path, euid); err != nil {
		return err
	}

	if perm := info.Mode().Perm(); perm&0o022 != 0 && info.Mode()&fs.ModeSticky == 0 {
		return fmt.Errorf("parent %s has mode %04o, is writable by group or other, and is not sticky", path, perm)
	}

	return nil
}

// checkOwner checks that info is owned by euid or by root.
func checkOwner(info fs.FileInfo, what, path string, euid int) error {
	name := strings.TrimSpace(what + " " + path)

	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot read the owner of %s", name)
	}

	if int64(st.Uid) != int64(euid) && st.Uid != 0 {
		return fmt.Errorf("%s is owned by uid %d, not this user's uid %d or root", name, st.Uid, euid)
	}

	return nil
}

// splitPath splits path into its names. A leading separator gives a first
// empty name, which the walk skips.
func splitPath(path string) []string {
	return strings.Split(path, string(filepath.Separator))
}
