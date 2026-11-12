package ownerrecord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/fivetwenty-io/ocfp-cli/internal/safedir"
)

// The owner record's place on a bastion.
const (
	// DefaultDir is the directory that holds the owner record.
	DefaultDir = "/data/ocfp-owner"

	// RecordFile is the signed owner record's file name in its directory.
	RecordFile = "cosmos-owner.json"

	// LockFile is the name of the file beside the record that every reader
	// and writer locks. It is never removed, so every caller locks the same
	// file.
	LockFile = "cosmos-owner.lock"
)

// The modes the store gives the files and the directory it creates.
const (
	dirMode    fs.FileMode = 0o750
	recordMode fs.FileMode = 0o640
	lockMode   fs.FileMode = 0o640
)

// maxRecordSize bounds how much of a stored record the store reads. A record
// is well under a kilobyte, so anything near this is not one.
const maxRecordSize = 1 << 20

var (
	// ErrNoRecord reports that the directory holds no owner record.
	ErrNoRecord = errors.New("no owner record")

	// ErrRecordChanged reports that the stored record is not at the
	// generation the caller expected, so another writer got there first.
	ErrRecordChanged = errors.New("owner record changed")

	// ErrStoredRecordInvalid reports that the stored record is not a signed
	// owner record or does not pass Open. It wraps the refusal that says
	// why, so it tells a bad stored record apart from a bad replacement.
	ErrStoredRecordInvalid = errors.New("stored owner record is not valid")

	// ErrUnsafeStore reports that a user other than this one or root could
	// change the record, its directory, or a directory above it. A record
	// names its own owner key, so a record that another user could replace
	// proves nothing. It wraps an OWNER_RECORD_INVALID refusal that says
	// what is unsafe.
	ErrUnsafeStore = errors.New("owner record store is not private")
)

// Store keeps one signed owner record.
type Store interface {
	// Read returns the stored record and its signed form. It returns
	// ErrNoRecord when there is none. When the stored record does not pass
	// Open, it returns the signed form with Open's refusal, so that the
	// caller can pass both to HandoverRefusal, and the Record is empty.
	Read(ctx context.Context) (Signed, Record, error)

	// RLock takes the shared lock and returns its release, so that no
	// Replace can finish while the caller acts on what it read.
	RLock(ctx context.Context) (func() error, error)

	// Replace stores next under the exclusive lock. A nil expect means
	// create only. Otherwise the stored record must be at generation
	// *expect, and next must be signed by the stored record's owner key and
	// be at generation *expect + 1.
	Replace(ctx context.Context, expect *uint64, next Signed) error
}

// DirStore is a Store that keeps the record as RecordFile in a directory and
// locks LockFile beside it. Read and RLock take a shared lock and Replace an
// exclusive one, with flock on Unix and LockFileEx on Windows. Locks are
// advisory, so they guard only against callers that lock too.
//
// The store sets the mode of the directory and files it creates, and it
// never changes a file's owner or group.
//
// Every use of the store first checks that no other user can change the
// record. The directory and the record must not be symbolic links, must be
// owned by this process's effective user or by root, and must not be
// writable by group or other. Every directory above them must pass
// safedir.Ancestors. A store that fails any of these is refused with
// ErrUnsafeStore, and nothing in it is read or changed.
type DirStore struct {
	dir string

	// euid, when set, is the user the store checks ownership against in
	// place of os.Geteuid.
	euid func() int

	// onWait, when set, is called each time a lock is taken elsewhere and
	// the store is about to wait for it.
	onWait func()

	// writeTemp, when set, writes the temporary record file in place of
	// (*os.File).Write.
	writeTemp func(f *os.File, b []byte) (int, error)

	// dirSync, when set, flushes a directory in place of syncDir.
	dirSync func(dir string) error
}

var _ Store = (*DirStore)(nil)

// NewDirStore returns a store for the record in dir. It cleans dir, so that
// a ".." climbs the names as written and not through a link, and every check
// and every use name the same directory. It touches nothing on disk until it
// is used.
func NewDirStore(dir string) *DirStore {
	if dir == "" {
		return &DirStore{}
	}

	return &DirStore{dir: filepath.Clean(dir)}
}

type lockKind int

const (
	lockShared lockKind = iota
	lockExclusive
)

// Read takes the shared lock, reads the record, and verifies it with Open.
// A stored record that is not a signed record, or that does not open, is
// reported with ErrStoredRecordInvalid wrapping the refusal. A store that
// another user could change is refused with ErrUnsafeStore.
func (s *DirStore) Read(ctx context.Context) (Signed, Record, error) {
	sf, err := s.readShared(ctx)
	if err != nil {
		return Signed{}, Record{}, fmt.Errorf("read owner record: %w", err)
	}

	r, err := Open(sf)
	if err != nil {
		return sf, Record{}, fmt.Errorf("read owner record: %w: %w", ErrStoredRecordInvalid, err)
	}

	return sf, r, nil
}

// readShared reads the stored record under the shared lock. The release is
// deferred so that a panic recovered further up cannot leave the lock held.
func (s *DirStore) readShared(ctx context.Context) (sf Signed, err error) {
	release, err := s.RLock(ctx)
	if err != nil {
		return Signed{}, err
	}

	defer func() {
		if rerr := release(); rerr != nil && err == nil {
			sf, err = Signed{}, rerr
		}
	}()

	return s.readSigned()
}

// RLock takes the shared lock. It never creates the directory, and it
// returns ErrNoRecord when the directory does not exist. It refuses a store
// that another user could change with ErrUnsafeStore. The release it
// returns may be called more than once, and only the first call acts.
func (s *DirStore) RLock(ctx context.Context) (func() error, error) {
	f, err := s.lock(ctx, lockShared)
	if err != nil {
		return nil, err
	}

	var once sync.Once

	var rerr error

	return func() error {
		once.Do(func() { rerr = release(f) })

		return rerr
	}, nil
}

// Replace verifies next with Open, takes the exclusive lock, checks next
// against the stored record, and then writes next to a temporary file in the
// directory, syncs it, renames it over RecordFile, and syncs the directory.
// A reader therefore sees the old record or the new one and never part of
// either. It creates the directory when it does not exist.
//
// Replace returns Open's refusal for a next that does not open. With a nil
// expect it refuses an existing record with OWNER_RECORD_EXISTS. Otherwise
// it returns ErrRecordChanged when the stored record is not at generation
// *expect, wrapping ErrNoRecord as well when there is no record. It refuses a
// next whose owner key is not the stored record's with OWNER_MISMATCH, and
// one whose generation is not *expect + 1 with JOB_REQUEST_INVALID. It
// replaces nothing while the stored record does not pass Open.
//
// A next whose stored form is larger than the store would read back is
// refused with JOB_REQUEST_INVALID before anything is written. A stored
// record that cannot be read as a signed record, or that does not open, is
// reported with ErrStoredRecordInvalid, which wraps the refusal beneath it.
// A store that another user could change is refused with ErrUnsafeStore,
// and Replace never creates a directory under a parent that fails
// safedir.Ancestors.
//
// While it holds the exclusive lock, Replace also removes temporary files
// that an earlier writer left behind when it stopped before its rename.
//
// An error from the final directory sync comes after the rename, so the new
// record is in place but may not survive a crash.
func (s *DirStore) Replace(ctx context.Context, expect *uint64, next Signed) (err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("replace owner record: %w", err)
		}
	}()

	if err := ctx.Err(); err != nil {
		return err
	}

	nr, err := Open(next)
	if err != nil {
		return err
	}

	data, err := encodeSigned(next)
	if err != nil {
		return err
	}

	if len(data) > maxRecordSize {
		return invalid("the owner record is %d bytes when stored, more than the %d the store reads", len(data), maxRecordSize)
	}

	if err := s.makeDir(); err != nil {
		return err
	}

	f, err := s.lock(ctx, lockExclusive)
	if err != nil {
		return err
	}

	// The release is deferred so that a panic recovered further up cannot
	// leave the exclusive lock held. The first error wins.
	defer func() {
		if rerr := release(f); rerr != nil && err == nil {
			err = rerr
		}
	}()

	s.sweepTemps()

	if err := s.checkReplace(expect, nr); err != nil {
		return err
	}

	return s.writeRecord(data)
}

// checkReplace checks next against the stored record. The caller holds the
// exclusive lock.
func (s *DirStore) checkReplace(expect *uint64, next Record) error {
	cur, err := s.readSigned()

	switch {
	case errors.Is(err, ErrNoRecord) && expect == nil:
		return nil
	case errors.Is(err, ErrNoRecord):
		return fmt.Errorf("expected generation %d: %w: %w", *expect, ErrRecordChanged, ErrNoRecord)
	case err != nil:
		return err
	case expect == nil:
		return &progress.Refusal{Code: progress.CodeOwnerRecordExists, Message: "an owner record already exists"}
	}

	stored, err := Open(cur)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrStoredRecordInvalid, err)
	}

	if stored.Generation != *expect {
		return fmt.Errorf("stored generation is %d, expected %d: %w", stored.Generation, *expect, ErrRecordChanged)
	}

	if !bytes.Equal(next.OwnerPublicKey, stored.OwnerPublicKey) {
		return &progress.Refusal{
			Code:    progress.CodeOwnerMismatch,
			Message: "the replacement is not signed by the stored record's owner key",
		}
	}

	if *expect == math.MaxUint64 || next.Generation != *expect+1 {
		return invalid("replacement generation is %d, want the generation after %d", next.Generation, *expect)
	}

	return nil
}

// readSigned reads and parses the stored record. It returns ErrNoRecord when
// there is none.
func (s *DirStore) readSigned() (Signed, error) {
	path := filepath.Join(s.dir, RecordFile)

	err := safedir.EntryAs(path, false, s.uid())
	if errors.Is(err, fs.ErrNotExist) {
		return Signed{}, ErrNoRecord
	}

	if err != nil {
		return Signed{}, unsafeStore(err)
	}

	// The directory is private, so only this user or root can swap the
	// record between the check and the open.
	f, err := os.Open(path) //nolint:gosec // the store's own record file
	if errors.Is(err, fs.ErrNotExist) {
		return Signed{}, ErrNoRecord
	}

	if err != nil {
		return Signed{}, fmt.Errorf("open owner record: %w", err)
	}

	data, rerr := io.ReadAll(io.LimitReader(f, maxRecordSize+1))
	cerr := f.Close()

	if rerr != nil {
		return Signed{}, fmt.Errorf("read owner record file: %w", rerr)
	}

	if cerr != nil {
		return Signed{}, fmt.Errorf("close owner record file: %w", cerr)
	}

	if len(data) > maxRecordSize {
		return Signed{}, fmt.Errorf("%w: %w", ErrStoredRecordInvalid,
			invalid("stored owner record is larger than %d bytes", maxRecordSize))
	}

	sf, err := Parse(data)
	if err != nil {
		return Signed{}, fmt.Errorf("%w: %w", ErrStoredRecordInvalid, err)
	}

	return sf, nil
}

// tempPrefix starts the name of every temporary record file. CreateTemp puts
// a decimal number after it.
const tempPrefix = "." + RecordFile + ".tmp-"

// sweepTemps removes temporary record files that an earlier writer left
// behind when it stopped between creating one and renaming it. The caller
// holds the exclusive lock, and every writer holds that lock while its
// temporary file exists, so none of these files is still being written. It
// removes only regular files whose names are exactly the store's own pattern,
// and it is best effort: a file it cannot remove is left for the next writer.
func (s *DirStore) sweepTemps() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}

	for _, e := range entries {
		if e.Type().IsRegular() && isTempName(e.Name()) {
			_ = os.Remove(filepath.Join(s.dir, e.Name()))
		}
	}
}

// isTempName reports whether name is tempPrefix followed by one or more
// decimal digits.
func isTempName(name string) bool {
	digits, ok := strings.CutPrefix(name, tempPrefix)
	if !ok || digits == "" {
		return false
	}

	for _, c := range digits {
		if c < '0' || c > '9' {
			return false
		}
	}

	return true
}

// syncDirectory flushes dir, through the test hook when one is set.
func (s *DirStore) syncDirectory(dir string) error {
	if s.dirSync != nil {
		return s.dirSync(dir)
	}

	return syncDir(dir)
}

// writeRecord writes data to a temporary file in the directory and renames
// it over RecordFile. The caller holds the exclusive lock.
func (s *DirStore) writeRecord(data []byte) error {
	tmp, err := os.CreateTemp(s.dir, tempPrefix+"*")
	if err != nil {
		return fmt.Errorf("create temporary owner record: %w", err)
	}

	name := tmp.Name()
	renamed := false

	defer func() {
		if !renamed {
			_ = os.Remove(name)
		}
	}()

	if err := s.fillTemp(tmp, data); err != nil {
		_ = tmp.Close()

		return err
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary owner record: %w", err)
	}

	if err := os.Rename(name, filepath.Join(s.dir, RecordFile)); err != nil {
		return fmt.Errorf("rename owner record into place: %w", err)
	}

	renamed = true

	return s.syncDirectory(s.dir)
}

// fillTemp writes data to tmp, sets its mode, and syncs it.
func (s *DirStore) fillTemp(tmp *os.File, data []byte) error {
	write := s.writeTemp
	if write == nil {
		write = (*os.File).Write
	}

	n, err := write(tmp, data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}

	if err != nil {
		return fmt.Errorf("write temporary owner record: %w", err)
	}

	if err := tmp.Chmod(recordMode); err != nil {
		return fmt.Errorf("set owner record mode: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary owner record: %w", err)
	}

	return nil
}

// makeDir checks the directories above the store, creates the directory
// with dirMode when it does not exist, and then syncs its parent so that the
// new directory's entry survives a crash. It leaves an existing directory's
// mode alone.
func (s *DirStore) makeDir() error {
	if s.dir == "" {
		return errNoDir
	}

	err := safedir.AncestorsAs(s.dir, s.uid())
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("create owner record directory: %w", err)
	}

	if err != nil {
		return unsafeStore(err)
	}

	err = os.Mkdir(s.dir, dirMode)
	if err == nil {
		// Mkdir's mode passes through the umask, so set it outright.
		if err := os.Chmod(s.dir, dirMode); err != nil {
			return fmt.Errorf("set owner record directory mode: %w", err)
		}

		return s.syncDirectory(filepath.Dir(s.dir))
	}

	if !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("create owner record directory: %w", err)
	}

	fi, err := os.Stat(s.dir)
	if err != nil {
		return fmt.Errorf("owner record directory: %w", err)
	}

	if !fi.IsDir() {
		return fmt.Errorf("owner record directory %s is not a directory", s.dir)
	}

	return nil
}

// errNoDir reports a store with no directory, which would otherwise use the
// working directory.
var errNoDir = errors.New("owner record store has no directory")

// uid is the user the store checks ownership against.
func (s *DirStore) uid() int {
	if s.euid != nil {
		return s.euid()
	}

	return os.Geteuid()
}

// unsafeStore reports err, which says how another user could change the
// store, as ErrUnsafeStore and an OWNER_RECORD_INVALID refusal.
func unsafeStore(err error) error {
	return fmt.Errorf("%w: %w", ErrUnsafeStore,
		&progress.Refusal{Code: progress.CodeOwnerRecordInvalid, Message: err.Error()})
}

// checkPrivate checks that no user but this one and root can change the
// directory, the record, or a directory above them. A missing directory, or
// a missing directory above it, is ErrNoRecord. A missing record is left
// for the caller to find.
func (s *DirStore) checkPrivate() error {
	if s.dir == "" {
		return errNoDir
	}

	uid := s.uid()

	err := safedir.AncestorsAs(s.dir, uid)
	if err == nil {
		err = safedir.EntryAs(s.dir, true, uid)
	}

	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("owner record directory %s does not exist: %w", s.dir, ErrNoRecord)
	}

	if err != nil {
		return unsafeStore(err)
	}

	err = safedir.EntryAs(filepath.Join(s.dir, RecordFile), false, uid)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return unsafeStore(err)
	}

	return nil
}

// lock opens LockFile, creating it when it does not exist, and takes a lock
// of the given kind on it. When the lock is taken elsewhere it waits until
// the lock is granted or ctx is done. It returns ErrNoRecord when the
// directory does not exist, and refuses with checkPrivate's error a store
// that another user could change.
func (s *DirStore) lock(ctx context.Context, kind lockKind) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("lock owner record: %w", err)
	}

	if err := s.checkPrivate(); err != nil {
		return nil, err
	}

	f, err := s.openLock(ctx)
	if err != nil {
		return nil, err
	}

	ok, err := tryLock(f, kind)
	if err != nil {
		_ = f.Close()

		return nil, fmt.Errorf("lock owner record: %w", err)
	}

	if ok {
		return f, nil
	}

	if s.onWait != nil {
		s.onWait()
	}

	return waitForLock(ctx, f, kind)
}

// waitForLock waits for the lock on f in another goroutine, because neither
// flock nor LockFileEx can be told to give up. When ctx is done first, that
// goroutine gives the lock back and closes f once it is granted.
func waitForLock(ctx context.Context, f *os.File, kind lockKind) (*os.File, error) {
	done := make(chan error, 1)

	go func() { done <- waitLock(f, kind) }()

	select {
	case err := <-done:
		if err != nil {
			_ = f.Close()

			return nil, fmt.Errorf("lock owner record: %w", err)
		}

		return f, nil
	case <-ctx.Done():
		go func() {
			if <-done == nil {
				_ = unlock(f)
			}

			_ = f.Close()
		}()

		return nil, fmt.Errorf("lock owner record: %w", ctx.Err())
	}
}

// maxLockOpens bounds how many times openLock goes round when the lock file
// keeps appearing and disappearing under it.
const maxLockOpens = 8

// openLock opens LockFile for locking. It creates the file with lockMode
// when it does not exist, but never the directory. It refuses, with
// JOB_REQUEST_INVALID, a lock path that is a symbolic link or anything else
// but a regular file, and it checks ctx on every pass.
func (s *DirStore) openLock(ctx context.Context) (*os.File, error) {
	if s.dir == "" {
		return nil, errNoDir
	}

	path := filepath.Join(s.dir, LockFile)

	for range maxLockOpens {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("lock owner record: %w", err)
		}

		fi, err := os.Lstat(path)

		switch {
		case err == nil:
			f, err := openExistingLock(path, fi)
			if errors.Is(err, fs.ErrNotExist) {
				// It went away between Lstat and Open, so look again.
				continue
			}

			return f, err
		case !errors.Is(err, fs.ErrNotExist):
			return nil, fmt.Errorf("owner record lock: %w", err)
		}

		f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE|os.O_EXCL, lockMode) //nolint:gosec // the store's own lock file
		if errors.Is(err, fs.ErrExist) {
			// Something else created the name first, so look at it again.
			continue
		}

		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("owner record directory %s does not exist: %w", s.dir, ErrNoRecord)
		}

		if err != nil {
			return nil, fmt.Errorf("create owner record lock: %w", err)
		}

		if err := f.Chmod(lockMode); err != nil {
			_ = f.Close()

			return nil, fmt.Errorf("set owner record lock mode: %w", err)
		}

		return f, nil
	}

	return nil, fmt.Errorf("owner record lock %s kept changing while it was opened", path)
}

// openExistingLock opens the lock file that Lstat described as fi. It refuses
// anything but a regular file before opening it, so that it never follows a
// link or blocks on a FIFO, and it checks afterwards that it opened that same
// file and not one swapped in between.
func openExistingLock(path string, fi fs.FileInfo) (*os.File, error) {
	if !fi.Mode().IsRegular() {
		return nil, invalid("owner record lock %s is not a regular file (mode %s)", path, fi.Mode().Type())
	}

	f, err := os.Open(path) //nolint:gosec // the store's own lock file
	if err != nil {
		return nil, fmt.Errorf("open owner record lock: %w", err)
	}

	opened, err := f.Stat()
	if err != nil {
		_ = f.Close()

		return nil, fmt.Errorf("owner record lock: %w", err)
	}

	if !os.SameFile(fi, opened) {
		_ = f.Close()

		return nil, invalid("owner record lock %s changed while it was opened", path)
	}

	return f, nil
}

// release gives back the lock on f and closes it.
func release(f *os.File) error {
	uerr := unlock(f)
	cerr := f.Close()

	if uerr != nil {
		return fmt.Errorf("unlock owner record: %w", uerr)
	}

	if cerr != nil {
		return fmt.Errorf("close owner record lock: %w", cerr)
	}

	return nil
}

// encodeSigned is the stored form of sf: indented JSON and a final newline.
func encodeSigned(sf Signed) ([]byte, error) {
	b, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode owner record: %w", err)
	}

	return append(b, '\n'), nil
}
