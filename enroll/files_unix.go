//go:build unix

package enroll

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// OpenNoFollow opens name, a path relative to dir, for reading under the
// file rules. It opens dir and every component without following a
// link, refuses anything but a regular file at the end, and refuses a
// file longer than limit. The caller closes the file. Every refusal
// wraps ErrFileRule and names the path.
func OpenNoFollow(dir, name string, limit int64) (*os.File, int64, error) {
	path := dir + "/" + name // unjoined, so a refusal names the path as given

	parts, ok := splitRelative(name)
	if !ok {
		return nil, 0, fmt.Errorf("%w: %s is not a plain relative path", ErrFileRule, path)
	}

	dirfd, err := openDirNoFollow(dir, parts[:len(parts)-1])
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %s: %w", ErrFileRule, path, err)
	}
	defer func() { _ = unix.Close(dirfd) }()

	fd, err := unix.Openat(dirfd, parts[len(parts)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %s: %w", ErrFileRule, path, err)
	}

	f := os.NewFile(uintptr(fd), path)

	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = f.Close()

		return nil, 0, fmt.Errorf("%w: %s: %w", ErrFileRule, path, err)
	}

	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = f.Close()

		return nil, 0, fmt.Errorf("%w: %s is not a regular file", ErrFileRule, path)
	}

	if st.Size > limit {
		_ = f.Close()

		return nil, 0, fmt.Errorf("%w: %s is longer than %d bytes", ErrFileRule, path, limit)
	}

	return f, st.Size, nil
}

// ReadNoFollow reads name, a path relative to dir, under the file rules,
// and refuses more than limit bytes even when the file grows while it
// reads.
func ReadNoFollow(dir, name string, limit int64) ([]byte, error) {
	f, _, err := OpenNoFollow(dir, name, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var buf bytes.Buffer

	n, err := io.Copy(&buf, io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrFileRule, f.Name(), err)
	}

	if n > limit {
		return nil, fmt.Errorf("%w: %s is longer than %d bytes", ErrFileRule, f.Name(), limit)
	}

	return buf.Bytes(), nil
}

// CreateExclusive creates name, a path relative to dir, as a fresh file
// under the file rules, writes data, and sets the owner and the mode on
// its own descriptor before it syncs and closes it. It refuses a name
// that already exists, of any kind. A uid or gid below zero leaves that
// id as the creating process's. When a step after the create fails, it
// removes the file it created.
func CreateExclusive(dir, name string, data []byte, uid, gid int, mode os.FileMode) error {
	path := dir + "/" + name // unjoined, so a refusal names the path as given

	parts, ok := splitRelative(name)
	if !ok {
		return fmt.Errorf("%w: %s is not a plain relative path", ErrFileRule, path)
	}

	dirfd, err := openDirNoFollow(dir, parts[:len(parts)-1])
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrFileRule, path, err)
	}
	defer func() { _ = unix.Close(dirfd) }()

	last := parts[len(parts)-1]

	fd, err := unix.Openat(dirfd, last, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrFileRule, path, err)
	}

	f := os.NewFile(uintptr(fd), path)

	if err := writeOwned(f, fd, data, uid, gid, mode); err != nil {
		_ = f.Close()
		_ = unix.Unlinkat(dirfd, last, 0)

		return fmt.Errorf("%w: %s: %w", ErrFileRule, path, err)
	}

	if err := f.Close(); err != nil {
		_ = unix.Unlinkat(dirfd, last, 0)

		return fmt.Errorf("%w: %s: %w", ErrFileRule, path, err)
	}

	return nil
}

func writeOwned(f *os.File, fd int, data []byte, uid, gid int, mode os.FileMode) error {
	if _, err := f.Write(data); err != nil {
		return err
	}

	if uid >= 0 || gid >= 0 {
		if err := unix.Fchown(fd, uid, gid); err != nil {
			return err
		}
	}

	if err := unix.Fchmod(fd, uint32(mode.Perm())); err != nil {
		return err
	}

	return f.Sync()
}

// openDirNoFollow opens dir and then each of parts beneath it as a
// directory, following no link at any step.
func openDirNoFollow(dir string, parts []string) (int, error) {
	const flags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC

	fd, err := unix.Open(dir, flags, 0)
	if err != nil {
		return -1, err
	}

	for _, p := range parts {
		next, err := unix.Openat(fd, p, flags, 0)
		_ = unix.Close(fd)

		if err != nil {
			return -1, err
		}

		fd = next
	}

	return fd, nil
}
