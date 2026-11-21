//go:build unix

package ownerrecord_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// These tests cover the record's old file name, which the store still reads
// for one release. They go away with LegacyRecordFile.

func legacyPath(dir string) string { return filepath.Join(dir, ownerrecord.LegacyRecordFile) }

// writeLegacy stores sf under the old file name in dir, as an earlier release
// left it.
func writeLegacy(t *testing.T, dir string, sf ownerrecord.Signed) {
	t.Helper()

	path := legacyPath(dir)
	if err := os.WriteFile(path, encode(t, sf), 0o640); err != nil {
		t.Fatal(err)
	}

	chmod(t, path, 0o640)
}

// legacyStore returns a store whose directory holds only sf under the old
// file name, as a bastion that an earlier release wrote does.
func legacyStore(t *testing.T, sf ownerrecord.Signed) (*ownerrecord.DirStore, string) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "owner")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}

	chmod(t, dir, 0o750)
	writeLegacy(t, dir, sf)

	return ownerrecord.NewDirStore(dir), dir
}

// opLog records, in order, the record removals and directory syncs a store
// makes. failOn names the files whose removal fails.
type opLog struct {
	ops    []string
	failOn map[string]bool
}

var errInjectedRemove = errors.New("injected remove failure")

func (l *opLog) attach(s *ownerrecord.DirStore) {
	ownerrecord.SetRecordRemover(s, func(path string) error {
		name := filepath.Base(path)
		l.ops = append(l.ops, "remove "+name)

		if l.failOn[name] {
			return errInjectedRemove
		}

		return os.Remove(path)
	})

	ownerrecord.SetDirSyncer(s, func(d string) error {
		l.ops = append(l.ops, "sync")

		return ownerrecord.SyncDir(d)
	})
}

func readGeneration(t *testing.T, s *ownerrecord.DirStore) uint64 {
	t.Helper()

	_, r, err := s.Read(t.Context())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	return r.Generation
}

func wantNoRecord(t *testing.T, s *ownerrecord.DirStore) {
	t.Helper()

	sf, r, err := s.Read(t.Context())
	if !errors.Is(err, ownerrecord.ErrNoRecord) {
		t.Fatalf("Read = generation %d (%d payload bytes), %v, want ErrNoRecord", r.Generation, len(sf.Payload), err)
	}
}

func releaseProofAt(t *testing.T, gen uint64) ownerrecord.Signed {
	t.Helper()

	r := liveRecord()
	r.Generation = gen

	return sealRelease(t, r)
}

// A bastion that an earlier release wrote holds only the old file, and it
// keeps working.
func TestReadFallsBackToLegacyRecord(t *testing.T) {
	t.Parallel()

	stored := liveAt(t, 7)
	s, dir := legacyStore(t, stored)

	sf, r, err := s.Read(t.Context())
	if err != nil {
		t.Fatalf("Read of a legacy record: %v", err)
	}

	if !signedEqual(sf, stored) || r.Generation != 7 {
		t.Fatalf("Read returned generation %d, want the legacy record at 7", r.Generation)
	}

	unlock, err := s.RLock(t.Context())
	if err != nil {
		t.Fatalf("RLock over a legacy record: %v", err)
	}

	if err := unlock(); err != nil {
		t.Fatal(err)
	}

	// Reading never moves or rewrites the old file.
	want := []string{ownerrecord.LegacyRecordFile, ownerrecord.LockFile}
	if got := dirNames(t, dir); !slices.Equal(got, want) {
		t.Errorf("after reading the directory holds %v, want %v", got, want)
	}
}

func TestReadPrefersRecordOverLegacy(t *testing.T) {
	t.Parallel()

	s, dir, _ := createdStore(t)
	writeLegacy(t, dir, liveAt(t, 3))

	if got := readGeneration(t, s); got != 7 {
		t.Fatalf("Read returned generation %d, want 7 from %s", got, ownerrecord.RecordFile)
	}
}

// Only a missing record lets the old file through. Any other trouble with
// the record is reported, and the old file is never read in its place.
func TestReadDoesNotFallBackPastABadRecord(t *testing.T) {
	t.Parallel()

	forged := liveAt(t, 7)
	forged.Signature = slices.Clone(forged.Signature)
	forged.Signature[0] ^= 0xff
	badSignature := encode(t, forged)

	cases := []struct {
		name    string
		arrange func(t *testing.T, path string)
		want    error
	}{
		{name: "a corrupt record", want: ownerrecord.ErrStoredRecordInvalid, arrange: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("{not a record"), 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a bad signature", want: ownerrecord.ErrStoredRecordInvalid, arrange: func(t *testing.T, path string) {
			if err := os.WriteFile(path, badSignature, 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a record others can write", want: ownerrecord.ErrUnsafeStore, arrange: func(t *testing.T, path string) {
			chmod(t, path, 0o646)
		}},
		{name: "a record that is a directory", want: ownerrecord.ErrUnsafeStore, arrange: func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}

			if err := os.Mkdir(path, 0o750); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "an unreadable record", arrange: func(t *testing.T, path string) {
			if os.Geteuid() == 0 {
				t.Skip("root reads a file whatever its mode")
			}

			chmod(t, path, 0o200)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			s, dir, _ := createdStore(t)
			legacy := liveAt(t, 7)
			writeLegacy(t, dir, legacy)
			tc.arrange(t, recordPath(dir))

			sf, r, err := s.Read(t.Context())
			if err == nil || errors.Is(err, ownerrecord.ErrNoRecord) {
				t.Fatalf("Read = generation %d, %v, want an error that is not ErrNoRecord", r.Generation, err)
			}

			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("Read = %v, want %v", err, tc.want)
			}

			if signedEqual(sf, legacy) {
				t.Fatal("Read returned the legacy record in place of the bad one")
			}

			// A writer sees the same trouble and changes nothing.
			if err := s.Replace(t.Context(), genPtr(7), liveAt(t, 8)); err == nil {
				t.Fatal("Replace over a bad record succeeded")
			}

			if !slices.Equal(readFile(t, legacyPath(dir)), encode(t, legacy)) {
				t.Error("the legacy record changed")
			}
		})
	}
}

// The old file is part of the store while it can still be read, so one that
// another user could change is refused like the record itself.
func TestStoreRefusesUnsafeLegacyRecord(t *testing.T) {
	t.Parallel()

	for _, withRecord := range []bool{false, true} {
		var (
			s   *ownerrecord.DirStore
			dir string
		)

		if withRecord {
			s, dir, _ = createdStore(t)
			writeLegacy(t, dir, liveAt(t, 7))
		} else {
			s, dir = legacyStore(t, liveAt(t, 7))
		}

		chmod(t, legacyPath(dir), 0o646)

		_, _, err := s.Read(t.Context())
		wantUnsafe(t, "Read", err)

		_, err = s.RLock(t.Context())
		wantUnsafe(t, "RLock", err)

		wantUnsafe(t, "Replace", s.Replace(t.Context(), genPtr(7), liveAt(t, 8)))
		wantUnsafe(t, "Release", s.Release(t.Context(), 7, releaseProofAt(t, 7)))

		if _, err := os.Stat(legacyPath(dir)); err != nil {
			t.Fatalf("the legacy record is gone: %v", err)
		}
	}
}

// A write moves a legacy record to the new name. The new record is flushed
// before the old file is removed, so a crash between the two never loses
// both.
func TestReplaceMovesLegacyRecord(t *testing.T) {
	t.Parallel()

	s, dir := legacyStore(t, liveAt(t, 7))

	// The legacy record is a record like any other.
	wantRefusal(t, s.Replace(t.Context(), nil, otherOwnerAt(t, 1)), progress.CodeOwnerRecordExists)

	if err := s.Replace(t.Context(), genPtr(6), liveAt(t, 7)); !errors.Is(err, ownerrecord.ErrRecordChanged) {
		t.Fatalf("replace at a stale generation = %v, want ErrRecordChanged", err)
	}

	var log opLog
	log.attach(s)

	if err := s.Replace(t.Context(), genPtr(7), liveAt(t, 8)); err != nil {
		t.Fatalf("replace of the legacy record: %v", err)
	}

	if want := []string{"sync", "remove " + ownerrecord.LegacyRecordFile, "sync"}; !slices.Equal(log.ops, want) {
		t.Errorf("the write did %v, want %v", log.ops, want)
	}

	if got := dirNames(t, dir); !slices.Equal(got, []string{ownerrecord.RecordFile, ownerrecord.LockFile}) {
		t.Errorf("after the write the directory holds %v", got)
	}

	if got := readGeneration(t, s); got != 8 {
		t.Fatalf("Read returned generation %d, want 8", got)
	}
}

func TestReplaceRemovesLeftoverLegacyRecord(t *testing.T) {
	t.Parallel()

	s, dir, _ := createdStore(t)
	writeLegacy(t, dir, liveAt(t, 7))

	if err := s.Replace(t.Context(), genPtr(7), liveAt(t, 8)); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(legacyPath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the legacy record survived a write: %v", err)
	}
}

// A refused write leaves the old file alone, because the record it would
// have replaced may be that file.
func TestRefusedReplaceKeepsLegacyRecord(t *testing.T) {
	t.Parallel()

	s, dir := legacyStore(t, liveAt(t, 7))

	if err := s.Replace(t.Context(), genPtr(6), liveAt(t, 7)); !errors.Is(err, ownerrecord.ErrRecordChanged) {
		t.Fatalf("err = %v, want ErrRecordChanged", err)
	}

	if got := readGeneration(t, s); got != 7 {
		t.Fatalf("Read returned generation %d, want 7", got)
	}

	if _, err := os.Lstat(recordPath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused write created %s: %v", ownerrecord.RecordFile, err)
	}
}

// The write is done once the new record is in place and flushed, so a failure
// to remove the old file afterwards does not fail it. The old file cannot
// come back while the record exists, and a release removes it first.
func TestReplaceKeepsItsWriteWhenLegacyRemovalFails(t *testing.T) {
	t.Parallel()

	s, dir, _ := createdStore(t)
	writeLegacy(t, dir, liveAt(t, 7))

	log := opLog{failOn: map[string]bool{ownerrecord.LegacyRecordFile: true}}
	log.attach(s)

	if err := s.Replace(t.Context(), genPtr(7), liveAt(t, 8)); err != nil {
		t.Fatalf("the write failed when only the old file's removal did: %v", err)
	}

	if got := readGeneration(t, s); got != 8 {
		t.Fatalf("Read returned generation %d, want 8", got)
	}

	if _, err := os.Stat(legacyPath(dir)); err != nil {
		t.Fatalf("the legacy record should still be there: %v", err)
	}

	// The released bastion must never read as owned through the old file.
	log.failOn = nil

	if err := s.Release(t.Context(), 8, releaseProofAt(t, 8)); err != nil {
		t.Fatal(err)
	}

	wantNoRecord(t, s)

	if got := dirNames(t, dir); !slices.Equal(got, []string{ownerrecord.LockFile}) {
		t.Errorf("after release the directory holds %v", got)
	}
}

// A release removes the record under both names. Were the old file left, the
// read would fall back to it and a released bastion would read as owned.
func TestReleaseRemovesBothNames(t *testing.T) {
	t.Parallel()

	s, dir, _ := createdStore(t)
	writeLegacy(t, dir, liveAt(t, 6))

	var log opLog
	log.attach(s)

	if err := s.Release(t.Context(), 7, releaseProofAt(t, 7)); err != nil {
		t.Fatal(err)
	}

	// The old file's removal is flushed before the record's, so a crash
	// can never leave the old file without the record above it.
	want := []string{
		"remove " + ownerrecord.LegacyRecordFile, "sync",
		"remove " + ownerrecord.RecordFile, "sync",
	}
	if !slices.Equal(log.ops, want) {
		t.Errorf("the release did %v, want %v", log.ops, want)
	}

	wantNoRecord(t, s)

	if got := dirNames(t, dir); !slices.Equal(got, []string{ownerrecord.LockFile}) {
		t.Errorf("after release the directory holds %v", got)
	}

	// The store is free for a new owner.
	if err := s.Replace(t.Context(), nil, otherOwnerAt(t, 1)); err != nil {
		t.Fatalf("a fresh write after release: %v", err)
	}
}

func TestReleaseRemovesLegacyOnlyRecord(t *testing.T) {
	t.Parallel()

	s, dir := legacyStore(t, liveAt(t, 7))

	if err := s.Release(t.Context(), 7, releaseProofAt(t, 7)); err != nil {
		t.Fatal(err)
	}

	wantNoRecord(t, s)

	if got := dirNames(t, dir); !slices.Equal(got, []string{ownerrecord.LockFile}) {
		t.Errorf("after release the directory holds %v", got)
	}
}

// A release that cannot remove a file fails, and the record it was asked to
// release still reads as it did. A retry finishes the job.
func TestReleaseFailsClosedWhenARemovalFails(t *testing.T) {
	t.Parallel()

	for _, failing := range []string{ownerrecord.LegacyRecordFile, ownerrecord.RecordFile} {
		t.Run(failing, func(t *testing.T) {
			t.Parallel()

			s, dir, _ := createdStore(t)
			writeLegacy(t, dir, liveAt(t, 6))

			log := opLog{failOn: map[string]bool{failing: true}}
			log.attach(s)

			err := s.Release(t.Context(), 7, releaseProofAt(t, 7))
			if !errors.Is(err, errInjectedRemove) {
				t.Fatalf("Release = %v, want the removal's failure", err)
			}

			if got := readGeneration(t, s); got != 7 {
				t.Fatalf("after a failed release Read returned generation %d, want 7", got)
			}

			log.failOn = nil

			if err := s.Release(t.Context(), 7, releaseProofAt(t, 7)); err != nil {
				t.Fatalf("retried release: %v", err)
			}

			wantNoRecord(t, s)

			if got := dirNames(t, dir); !slices.Equal(got, []string{ownerrecord.LockFile}) {
				t.Errorf("after release the directory holds %v", got)
			}
		})
	}
}

// New temporary files take the record's new name.
func TestReplaceNamesTemporaryFilesAfterRecord(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "owner")
	s := ownerrecord.NewDirStore(dir)

	var temps []string

	ownerrecord.SetTempWriter(s, func(f *os.File, b []byte) (int, error) {
		temps = append(temps, filepath.Base(f.Name()))

		return f.Write(b)
	})

	if err := s.Replace(t.Context(), nil, liveAt(t, 7)); err != nil {
		t.Fatal(err)
	}

	if len(temps) != 1 || !strings.HasPrefix(temps[0], "."+ownerrecord.RecordFile+".tmp-") {
		t.Fatalf("temporary files %v, want one named .%s.tmp-<n>", temps, ownerrecord.RecordFile)
	}
}

// A store from CurrentOnly reads the record only under RecordFile, so a
// directory that holds only the old file holds no record for it, while the
// store it came from still falls back.
func TestCurrentOnlyNeverReadsLegacyRecord(t *testing.T) {
	t.Parallel()

	s, dir := legacyStore(t, liveAt(t, 7))
	strict := s.CurrentOnly()

	wantNoRecord(t, strict)

	if got := readGeneration(t, s); got != 7 {
		t.Fatalf("the store CurrentOnly came from read generation %d, want the legacy record at 7", got)
	}

	wantNoRecord(t, ownerrecord.NewDirStore(dir).CurrentOnly().CurrentOnly())
}

func TestCurrentOnlyReadsRecordFile(t *testing.T) {
	t.Parallel()

	s, dir, _ := createdStore(t)
	writeLegacy(t, dir, liveAt(t, 3))

	if got := readGeneration(t, s.CurrentOnly()); got != 7 {
		t.Fatalf("Read returned generation %d, want 7 from %s", got, ownerrecord.RecordFile)
	}

	// The old file is still part of the store, so one that another user
	// could change is refused here too.
	chmod(t, legacyPath(dir), 0o646)

	_, _, err := s.CurrentOnly().Read(t.Context())
	wantUnsafe(t, "Read", err)
}

// A store from CurrentOnly locks the same file as the store it came from.
func TestCurrentOnlySharesTheLock(t *testing.T) {
	t.Parallel()

	s, dir := legacyStore(t, liveAt(t, 7))

	unlock, err := s.CurrentOnly().RLock(t.Context())
	if err != nil {
		t.Fatalf("RLock: %v", err)
	}

	lockPath := filepath.Join(dir, ownerrecord.LockFile)

	if tryExclusive(t, lockPath) {
		t.Fatal("an exclusive lock was granted under the shared lock")
	}

	if err := unlock(); err != nil {
		t.Fatal(err)
	}

	if !tryExclusive(t, lockPath) {
		t.Fatal("the exclusive lock was not granted after release")
	}
}
