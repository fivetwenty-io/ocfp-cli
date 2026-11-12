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
//
// A caller that goes on to use path should call ResolveAs instead and use
// the path it returns, so that the directories it uses are the ones that
// were checked.
func AncestorsAs(path string, euid int) error {
	_, err := ResolveAs(path, euid)

	return err
}

// Resolve checks every directory above path as this process's effective
// user and returns path with them resolved. See ResolveAs.
func Resolve(path string) (string, error) {
	return ResolveAs(path, os.Geteuid())
}

// ResolveAs makes every check that AncestorsAs makes and returns path with
// every symbolic link above its last name resolved. It then resolves the
// parent again with filepath.EvalSymlinks, refuses a result that differs
// from the walk's, and checks each directory of that result once more, so
// that the parent it returns holds no symbolic link and every directory in
// it passed the checks.
//
// The last name is kept as written and never followed, so a symbolic link
// there stays a link for EntryAs to refuse, and a last name that does not
// exist yet is fine. A parent that does not exist gives an error that wraps
// fs.ErrNotExist. On any error the path returned is empty.
//
// The caller must use the returned path, and never path, for everything it
// reads, writes, or creates. No user but euid and root can rename, remove,
// or replace a directory in it, so a link above path that changes after the
// check cannot move what the caller uses.
func ResolveAs(path string, euid int) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}

	walked, err := walkParents(path, filepath.Dir(abs), euid)
	if err != nil {
		return "", err
	}

	if abs == string(filepath.Separator) {
		return abs, nil
	}

	resolved, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", fmt.Errorf("check parents of %s: %w", path, err)
	}

	if resolved != walked {
		return "", fmt.Errorf("parents of %s resolved to %s and then to %s while they were checked", path, walked, resolved)
	}

	if err := checkResolved(path, resolved, euid); err != nil {
		return "", err
	}

	return filepath.Join(resolved, filepath.Base(abs)), nil
}

// checkResolved checks each directory of dir, which holds no symbolic link,
// from / down. A name in it that has become a link since dir was resolved is
// refused.
func checkResolved(path, dir string, euid int) error {
	cur := string(filepath.Separator)

	for _, name := range splitPath(dir) {
		if name == "" {
			continue
		}

		cur = filepath.Join(cur, name)

		info, err := os.Lstat(cur)
		if err != nil {
			return fmt.Errorf("check parents of %s: %w", path, err)
		}

		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("parent %s of %s became a symbolic link while it was checked", cur, path)
		case !info.IsDir():
			return fmt.Errorf("parent %s of %s is not a directory", cur, path)
		}

		if err := checkParent(info, cur, euid); err != nil {
			return err
		}
	}

	return nil
}

// walkParents checks / and then each name of parent, the directory above
// path, as AncestorsAs describes, and returns the directory the walk ends
// in, which holds no symbolic link.
func walkParents(path, parent string, euid int) (string, error) {
	cur := string(filepath.Separator)

	info, err := os.Lstat(cur)
	if err != nil {
		return "", fmt.Errorf("check parents of %s: %w", path, err)
	}

	if err := checkParent(info, cur, euid); err != nil {
		return "", err
	}

	pending := splitPath(parent)
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
			return "", fmt.Errorf("check parents of %s: %w", path, err)
		}

		if info.Mode()&fs.ModeSymlink != 0 {
			links++
			if links > maxLinks {
				return "", fmt.Errorf("parents of %s pass through more than %d symbolic links", path, maxLinks)
			}

			if err := checkOwner(info, "symbolic link", next, euid); err != nil {
				return "", err
			}

			target, err := os.Readlink(next)
			if err != nil {
				return "", fmt.Errorf("check parents of %s: %w", path, err)
			}

			if filepath.IsAbs(target) {
				cur = string(filepath.Separator)
			}

			pending = append(splitPath(target), pending...)

			continue
		}

		if !info.IsDir() {
			return "", fmt.Errorf("parent %s of %s is not a directory", next, path)
		}

		if err := checkParent(info, next, euid); err != nil {
			return "", err
		}

		cur = next
	}

	return cur, nil
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
