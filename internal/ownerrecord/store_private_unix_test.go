//go:build unix

package ownerrecord_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// privateStore makes a store in parent/owner, where parent is private, that
// holds the live record at generation 7.
func privateStore(t *testing.T) (parent, dir string) {
	t.Helper()

	parent = filepath.Join(t.TempDir(), "parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}

	dir = filepath.Join(parent, "owner")

	if err := ownerrecord.NewDirStore(dir).Replace(t.Context(), nil, liveAt(t, 7)); err != nil {
		t.Fatal(err)
	}

	return parent, dir
}

func chmod(t *testing.T, path string, mode fs.FileMode) {
	t.Helper()

	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func wantUnsafe(t *testing.T, op string, err error) {
	t.Helper()

	if !errors.Is(err, ownerrecord.ErrUnsafeStore) {
		t.Fatalf("%s = %v, want ErrUnsafeStore", op, err)
	}

	wantRefusal(t, err, progress.CodeOwnerRecordInvalid)
}

// A record that another user could replace proves nothing, because anyone can
// sign a record that names their own key. Every use of a store that another
// user could change is refused, and the record is left as it was.
func TestStoreRefusesWhatAnotherUserCouldChange(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// arrange changes the store in parent/owner and returns the
		// directory path the store is opened on.
		arrange func(t *testing.T, parent, dir string) string
		euid    func() (int, bool)
	}{
		{name: "a linked directory", arrange: func(t *testing.T, parent, dir string) string {
			link := filepath.Join(parent, "link")
			if err := os.Symlink(dir, link); err != nil {
				t.Fatal(err)
			}

			return link
		}},
		{name: "a linked record", arrange: func(t *testing.T, _, dir string) string {
			path := filepath.Join(dir, ownerrecord.RecordFile)
			moved := filepath.Join(dir, "moved.json")

			if err := os.Rename(path, moved); err != nil {
				t.Fatal(err)
			}

			if err := os.Symlink(moved, path); err != nil {
				t.Fatal(err)
			}

			return dir
		}},
		{name: "a group-writable directory", arrange: func(t *testing.T, _, dir string) string {
			chmod(t, dir, 0o770)

			return dir
		}},
		{name: "an other-writable directory", arrange: func(t *testing.T, _, dir string) string {
			chmod(t, dir, 0o757)

			return dir
		}},
		{name: "a group-writable record", arrange: func(t *testing.T, _, dir string) string {
			chmod(t, filepath.Join(dir, ownerrecord.RecordFile), 0o660)

			return dir
		}},
		{name: "an other-writable record", arrange: func(t *testing.T, _, dir string) string {
			chmod(t, filepath.Join(dir, ownerrecord.RecordFile), 0o646)

			return dir
		}},
		{name: "a writable directory reached through a link and ..", arrange: func(t *testing.T, parent, dir string) string {
			// link/.. leads the kernel to x, which holds a private owner
			// directory, while the path names parent/owner, which is open
			// to everyone.
			chmod(t, dir, 0o777)

			for _, d := range []string{"x", filepath.Join("x", "sub"), filepath.Join("x", "owner")} {
				if err := os.Mkdir(filepath.Join(parent, d), 0o700); err != nil {
					t.Fatal(err)
				}
			}

			if err := os.Symlink(filepath.Join(parent, "x", "sub"), filepath.Join(parent, "link")); err != nil {
				t.Fatal(err)
			}

			// filepath.Join would clean the path, so build it by hand.
			return parent + "/link/../owner"
		}},
		{name: "a writable parent", arrange: func(t *testing.T, parent, dir string) string {
			chmod(t, parent, 0o777)

			return dir
		}},
		{
			name:    "another owner",
			arrange: func(_ *testing.T, _, dir string) string { return dir },
			euid: func() (int, bool) {
				euid := os.Geteuid()

				return euid + 1, euid != 0
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parent, dir := privateStore(t)
			path := filepath.Join(dir, ownerrecord.RecordFile)

			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			open := tc.arrange(t, parent, dir)
			s := ownerrecord.NewDirStore(open)

			if tc.euid != nil {
				euid, ok := tc.euid()
				if !ok {
					t.Skip("root owns whatever this test makes")
				}

				ownerrecord.SetEUID(s, euid)
			}

			_, _, err = s.Read(t.Context())
			wantUnsafe(t, "Read", err)

			_, err = s.RLock(t.Context())
			wantUnsafe(t, "RLock", err)

			expect := uint64(7)
			wantUnsafe(t, "Replace", s.Replace(t.Context(), &expect, liveAt(t, 8)))

			owner := liveRecord()
			owner.Generation = 7
			wantUnsafe(t, "Release", s.Release(t.Context(), 7, sealRelease(t, owner)))

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("the record is gone: %v", err)
			}

			if !bytes.Equal(before, after) {
				t.Fatal("the record changed")
			}
		})
	}
}

// A first write under a parent that others can write is refused before the
// directory is made.
func TestReplaceMakesNoDirectoryUnderAWritableParent(t *testing.T) {
	t.Parallel()

	parent := filepath.Join(t.TempDir(), "parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}

	chmod(t, parent, 0o777)

	dir := filepath.Join(parent, "owner")

	wantUnsafe(t, "Replace", ownerrecord.NewDirStore(dir).Replace(t.Context(), nil, liveAt(t, 7)))

	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Replace made the directory: %v", err)
	}
}

// A missing directory is still no record, once its parents are known to be
// safe.
func TestStoreWithAMissingDirectory(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "gone", "owner")

	if _, _, err := ownerrecord.NewDirStore(missing).Read(t.Context()); !errors.Is(err, ownerrecord.ErrNoRecord) {
		t.Fatalf("Read under a missing parent = %v, want ErrNoRecord", err)
	}
}

// storedGeneration opens the record in dir and returns its generation.
func storedGeneration(t *testing.T, dir string) uint64 {
	t.Helper()

	r, err := ownerrecord.Open(mustParse(t, readFile(t, recordPath(dir))))
	if err != nil {
		t.Fatal(err)
	}

	return r.Generation
}

// A store whose parent is reached through a link that this user owns, as a
// home directory on a data disk is, works where the link leads.
func TestStoreThroughALinkedParent(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	data := filepath.Join(base, "data")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(data, filepath.Join(base, "home")); err != nil {
		t.Fatal(err)
	}

	s := ownerrecord.NewDirStore(filepath.Join(base, "home", "owner"))

	if err := s.Replace(t.Context(), nil, liveAt(t, 7)); err != nil {
		t.Fatalf("Replace through a private link: %v", err)
	}

	if got := storedGeneration(t, filepath.Join(data, "owner")); got != 7 {
		t.Fatalf("stored generation = %d, want 7", got)
	}

	if _, r, err := s.Read(t.Context()); err != nil || r.Generation != 7 {
		t.Fatalf("Read through a private link = generation %d, %v; want 7", r.Generation, err)
	}

	owner := liveRecord()
	owner.Generation = 7

	if err := s.Release(t.Context(), 7, sealRelease(t, owner)); err != nil {
		t.Fatalf("Release through a private link: %v", err)
	}

	if _, err := os.Lstat(recordPath(filepath.Join(data, "owner"))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Release left the record: %v", err)
	}
}

// Once Replace has checked the store and taken its lock, it works in that
// directory. Repointing a link above it partway through must not send the
// record to a directory that was neither checked nor locked.
func TestReplaceWritesWhereItChecked(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	first := filepath.Join(base, "first")
	second := filepath.Join(base, "second")

	for _, d := range []string{first, second} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}

		if err := ownerrecord.NewDirStore(filepath.Join(d, "owner")).Replace(t.Context(), nil, liveAt(t, 7)); err != nil {
			t.Fatal(err)
		}
	}

	before := readFile(t, recordPath(filepath.Join(second, "owner")))

	link := filepath.Join(base, "current")
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}

	s := ownerrecord.NewDirStore(filepath.Join(link, "owner"))

	ownerrecord.SetTempWriter(s, func(f *os.File, b []byte) (int, error) {
		if err := os.Remove(link); err != nil {
			return 0, err
		}

		if err := os.Symlink(second, link); err != nil {
			return 0, err
		}

		return f.Write(b)
	})

	if err := s.Replace(t.Context(), genPtr(7), liveAt(t, 8)); err != nil {
		t.Fatalf("Replace after the link moved: %v", err)
	}

	if got := storedGeneration(t, filepath.Join(first, "owner")); got != 8 {
		t.Fatalf("the checked store holds generation %d, want 8", got)
	}

	if !bytes.Equal(readFile(t, recordPath(filepath.Join(second, "owner"))), before) {
		t.Fatal("the record reached the directory the link was moved to")
	}
}

// Release removes the record from the directory it checked and locked, even
// when a link above the store is moved while it waits for the lock.
func TestReleaseRemovesWhereItChecked(t *testing.T) {
	t.Parallel()

	base := t.TempDir()

	first := filepath.Join(base, "first")
	second := filepath.Join(base, "second")

	for _, d := range []string{first, second} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}

		if err := ownerrecord.NewDirStore(filepath.Join(d, "owner")).Replace(t.Context(), nil, liveAt(t, 7)); err != nil {
			t.Fatal(err)
		}
	}

	link := filepath.Join(base, "current")
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}

	unlock, ok, err := ownerrecord.TryLockExclusive(filepath.Join(first, "owner", ownerrecord.LockFile))
	if err != nil || !ok {
		t.Fatalf("hold the lock: %v, %v", ok, err)
	}

	s := ownerrecord.NewDirStore(filepath.Join(link, "owner"))

	var once sync.Once

	ownerrecord.SetLockWaitHook(s, func() {
		once.Do(func() {
			if err := os.Remove(link); err != nil {
				t.Error(err)
			}

			if err := os.Symlink(second, link); err != nil {
				t.Error(err)
			}

			if err := unlock(); err != nil {
				t.Error(err)
			}
		})
	})

	owner := liveRecord()
	owner.Generation = 7

	if err := s.Release(t.Context(), 7, sealRelease(t, owner)); err != nil {
		t.Fatalf("Release after the link moved: %v", err)
	}

	if _, err := os.Lstat(recordPath(filepath.Join(first, "owner"))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the checked store kept its record: %v", err)
	}

	if got := storedGeneration(t, filepath.Join(second, "owner")); got != 7 {
		t.Fatalf("the store the link was moved to holds generation %d, want 7", got)
	}
}
