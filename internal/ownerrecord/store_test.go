package ownerrecord_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord/ownerrecordtest"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// The store must satisfy the interface its callers take.
var _ ownerrecord.Store = (*ownerrecord.DirStore)(nil)

func TestStoreNames(t *testing.T) {
	t.Parallel()

	if ownerrecord.DefaultDir != "/data/ocfp-owner" {
		t.Errorf("DefaultDir = %q", ownerrecord.DefaultDir)
	}

	if ownerrecord.RecordFile != "cosmos-owner.json" {
		t.Errorf("RecordFile = %q", ownerrecord.RecordFile)
	}

	if ownerrecord.LockFile != "cosmos-owner.lock" {
		t.Errorf("LockFile = %q", ownerrecord.LockFile)
	}
}

// liveAt is the live record at generation gen.
func liveAt(t *testing.T, gen uint64) ownerrecord.Signed {
	t.Helper()

	r := liveRecord()
	r.Generation = gen

	return seal(t, r)
}

// otherOwnerAt is a live record at generation gen whose owner is the target
// instance, signed by the target's own key.
func otherOwnerAt(t *testing.T, gen uint64) ownerrecord.Signed {
	t.Helper()

	r := liveRecord()
	r.OwnerInstanceID = "instance-fixture-b"
	r.SignerID = "instance-fixture-b"
	r.OwnerPublicKey = targetKey.Public().(ed25519.PublicKey)
	r.Generation = gen

	sf, err := ownerrecord.Seal(r, func(msg []byte) ([]byte, error) {
		return ed25519.Sign(targetKey, msg), nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return sf
}

// encode is the stored form of sf, written by hand to set up a test.
func encode(t *testing.T, sf ownerrecord.Signed) []byte {
	t.Helper()

	b, err := json.Marshal(sf)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func genPtr(g uint64) *uint64 { return &g }

func recordPath(dir string) string { return filepath.Join(dir, ownerrecord.RecordFile) }

func readFile(t *testing.T, path string) []byte {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// createdStore returns a store whose directory holds the live record at
// generation 7, and the stored bytes.
func createdStore(t *testing.T) (*ownerrecord.DirStore, string, []byte) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "owner")
	s := ownerrecord.NewDirStore(dir)

	if err := s.Replace(t.Context(), nil, liveAt(t, 7)); err != nil {
		t.Fatal(err)
	}

	return s, dir, readFile(t, recordPath(dir))
}

func signedEqual(a, b ownerrecord.Signed) bool {
	return a.Format == b.Format && bytes.Equal(a.Payload, b.Payload) && bytes.Equal(a.Signature, b.Signature)
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}

	slices.Sort(names)

	return names
}

func TestReadWithoutRecord(t *testing.T) {
	t.Parallel()

	empty := t.TempDir()

	sf, r, err := ownerrecord.NewDirStore(empty).Read(t.Context())
	if !errors.Is(err, ownerrecord.ErrNoRecord) {
		t.Fatalf("Read of an empty directory: err = %v, want ErrNoRecord", err)
	}

	if sf.Payload != nil || r.InstallationID != "" {
		t.Errorf("Read returned a record with ErrNoRecord: %+v %+v", sf, r)
	}

	// A directory that does not exist has no record either, and a reader
	// never creates it.
	missing := filepath.Join(t.TempDir(), "absent")

	if _, _, err := ownerrecord.NewDirStore(missing).Read(t.Context()); !errors.Is(err, ownerrecord.ErrNoRecord) {
		t.Fatalf("Read of a missing directory: err = %v, want ErrNoRecord", err)
	}

	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Read created the directory: %v", err)
	}
}

func TestReplaceCreateOnly(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "owner")
	s := ownerrecord.NewDirStore(dir)
	first := liveAt(t, 7)

	if err := s.Replace(t.Context(), nil, first); err != nil {
		t.Fatal(err)
	}

	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{
			dir:                                      0o750,
			recordPath(dir):                          0o640,
			filepath.Join(dir, ownerrecord.LockFile): 0o640,
		} {
			fi, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}

			if got := fi.Mode().Perm(); got != want {
				t.Errorf("%s has mode %#o, want %#o", filepath.Base(path), got, want)
			}
		}
	}

	sf, r, err := s.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if !signedEqual(sf, first) || r.Generation != 7 {
		t.Errorf("Read returned %+v at generation %d, want the record just written", sf, r.Generation)
	}

	stored := readFile(t, recordPath(dir))

	parsed, err := ownerrecord.Parse(stored)
	if err != nil || !signedEqual(parsed, first) {
		t.Errorf("the stored file does not parse back to the record written: %v", err)
	}

	// A second create-only write is refused while a record exists, whoever
	// signed it.
	for _, next := range []ownerrecord.Signed{liveAt(t, 8), otherOwnerAt(t, 1)} {
		wantRefusal(t, s.Replace(t.Context(), nil, next), progress.CodeOwnerRecordExists)
	}

	if !bytes.Equal(readFile(t, recordPath(dir)), stored) {
		t.Error("a refused create-only write changed the record")
	}

	if got := dirNames(t, dir); !slices.Equal(got, []string{ownerrecord.RecordFile, ownerrecord.LockFile}) {
		t.Errorf("directory holds %v", got)
	}
}

func TestReplaceNeedsCurrentGeneration(t *testing.T) {
	t.Parallel()

	s, dir, stored := createdStore(t)

	for _, expect := range []uint64{0, 6, 8} {
		err := s.Replace(t.Context(), genPtr(expect), liveAt(t, expect+1))
		if !errors.Is(err, ownerrecord.ErrRecordChanged) {
			t.Errorf("expect %d against generation 7: err = %v, want ErrRecordChanged", expect, err)
		}
	}

	if !bytes.Equal(readFile(t, recordPath(dir)), stored) {
		t.Error("a refused replace changed the record")
	}

	if err := s.Replace(t.Context(), genPtr(7), liveAt(t, 8)); err != nil {
		t.Fatalf("replace at the current generation: %v", err)
	}

	if _, r, err := s.Read(t.Context()); err != nil || r.Generation != 8 {
		t.Errorf("after replace: generation %d, err %v, want 8", r.Generation, err)
	}

	// An expected generation with no record at all is a change too, and says
	// that the record is gone.
	empty := ownerrecord.NewDirStore(t.TempDir())

	err := empty.Replace(t.Context(), genPtr(7), liveAt(t, 8))
	if !errors.Is(err, ownerrecord.ErrRecordChanged) || !errors.Is(err, ownerrecord.ErrNoRecord) {
		t.Errorf("expect with no record: err = %v, want ErrRecordChanged and ErrNoRecord", err)
	}

	if _, _, err := empty.Read(t.Context()); !errors.Is(err, ownerrecord.ErrNoRecord) {
		t.Errorf("a refused replace wrote a record: %v", err)
	}
}

func TestReplaceMustBeSignedByCurrentOwner(t *testing.T) {
	t.Parallel()

	s, dir, stored := createdStore(t)

	// Signed by its own owner, so it opens, but that owner is not the one
	// the stored record names.
	wantRefusal(t, s.Replace(t.Context(), genPtr(7), otherOwnerAt(t, 8)), progress.CodeOwnerMismatch)

	// The current owner signed it, but the generation is not the next one.
	for _, gen := range []uint64{6, 7, 9, 1 << 40} {
		wantRefusal(t, s.Replace(t.Context(), genPtr(7), liveAt(t, gen)), progress.CodeJobRequestInvalid)
	}

	// A replacement that does not open is refused before anything else.
	bad := liveAt(t, 8)
	bad.Signature = bytes.Clone(bad.Signature)
	bad.Signature[0] ^= 0x01
	wantRefusal(t, s.Replace(t.Context(), genPtr(7), bad), progress.CodeRequestUnsigned)

	// A hand-over signed by the instance handing ownership over does not
	// pass Open, so the store refuses it.
	wantRefusal(t, s.Replace(t.Context(), genPtr(7), sealHandover(t)), progress.CodeRequestUnsigned)

	if !bytes.Equal(readFile(t, recordPath(dir)), stored) {
		t.Error("a refused replace changed the record")
	}
}

func TestReplaceRefusesTheLastGeneration(t *testing.T) {
	t.Parallel()

	s, dir, _ := createdStore(t)

	maxGen := ^uint64(0)

	// Move the stored record to the last generation by hand, as no chain of
	// replaces can reach it in a test.
	last := liveAt(t, maxGen)
	if err := os.WriteFile(recordPath(dir), encode(t, last), 0o640); err != nil {
		t.Fatal(err)
	}

	// expect + 1 wraps to 0, which must never count as the next generation.
	wantRefusal(t, s.Replace(t.Context(), genPtr(maxGen), liveAt(t, 0)), progress.CodeJobRequestInvalid)
}

func TestReplaceWaitsForSharedLock(t *testing.T) {
	t.Parallel()

	s, dir, _ := createdStore(t)
	lockPath := filepath.Join(dir, ownerrecord.LockFile)

	release, err := s.RLock(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	// The shared lock is really held on the lock file.
	if ok := tryExclusive(t, lockPath); ok {
		t.Fatal("an exclusive lock was granted while RLock was held")
	}

	// A reader can still read while another shared lock is held.
	if _, r, err := s.Read(t.Context()); err != nil || r.Generation != 7 {
		t.Fatalf("Read under a shared lock: generation %d, err %v", r.Generation, err)
	}

	waiting := make(chan struct{})
	ownerrecord.SetLockWaitHook(s, func() { close(waiting) })

	// While Replace writes its temporary file it must hold the exclusive
	// lock, so a shared attempt made at that moment must be refused. This
	// catches a writer that went on without really waiting, whatever the
	// timing.
	unguarded := make(chan error, 1)

	ownerrecord.SetTempWriter(s, func(f *os.File, b []byte) (int, error) {
		ok, err := ownerrecord.TryLockShared(lockPath)
		if err == nil && ok {
			err = errors.New("a shared lock was granted while Replace was writing")
		}

		unguarded <- err

		return f.Write(b)
	})

	next := liveAt(t, 8)
	starting := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		close(starting)
		done <- s.Replace(context.Background(), genPtr(7), next)
	}()

	<-starting

	// Either Replace finds the lock taken and is about to block on it, or it
	// returns without waiting, which is the bug this test looks for.
	select {
	case <-waiting:
	case err := <-done:
		t.Fatalf("Replace returned without waiting for the shared lock: %v", err)
	}

	select {
	case err := <-done:
		t.Fatalf("Replace returned while a shared lock was held: %v", err)
	default:
	}

	if r, err := ownerrecord.Open(mustParse(t, readFile(t, recordPath(dir)))); err != nil || r.Generation != 7 {
		t.Fatalf("the record changed while a shared lock was held: generation %d, err %v", r.Generation, err)
	}

	if err := release(); err != nil {
		t.Fatal(err)
	}

	if err := <-done; err != nil {
		t.Fatalf("Replace after the shared lock was released: %v", err)
	}

	if err := <-unguarded; err != nil {
		t.Fatalf("Replace wrote without holding the exclusive lock: %v", err)
	}

	if _, r, err := s.Read(t.Context()); err != nil || r.Generation != 8 {
		t.Errorf("after the wait: generation %d, err %v, want 8", r.Generation, err)
	}

	// Every lock is released once Replace has returned.
	if ok := tryExclusive(t, lockPath); !ok {
		t.Error("the lock file is still locked after Replace returned")
	}

	// Releasing twice is harmless.
	if err := release(); err != nil {
		t.Errorf("second release: %v", err)
	}
}

func TestReplaceStopsWaitingWhenCancelled(t *testing.T) {
	t.Parallel()

	s, _, _ := createdStore(t)

	release, err := s.RLock(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	ownerrecord.SetLockWaitHook(s, cancel)

	if err := s.Replace(ctx, genPtr(7), liveAt(t, 8)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Replace while cancelled: err = %v, want context.Canceled", err)
	}

	ownerrecord.SetLockWaitHook(s, nil)

	if err := release(); err != nil {
		t.Fatal(err)
	}

	// The abandoned wait gives its lock back, so a later write goes ahead.
	if err := s.Replace(t.Context(), genPtr(7), liveAt(t, 8)); err != nil {
		t.Fatalf("Replace after a cancelled wait: %v", err)
	}
}

func TestReplaceIsAtomic(t *testing.T) {
	t.Parallel()

	failing := func(f *os.File, b []byte) (int, error) {
		n, err := f.Write(b[:len(b)/2])
		if err != nil {
			return n, err
		}

		return n, errors.New("disk full")
	}

	// A failed replace leaves the old record whole.
	s, dir, stored := createdStore(t)
	ownerrecord.SetTempWriter(s, failing)

	if err := s.Replace(t.Context(), genPtr(7), liveAt(t, 8)); err == nil {
		t.Fatal("Replace reported success when its write failed")
	}

	if !bytes.Equal(readFile(t, recordPath(dir)), stored) {
		t.Error("a failed replace left a changed record")
	}

	if _, r, err := s.Read(t.Context()); err != nil || r.Generation != 7 {
		t.Errorf("after a failed replace: generation %d, err %v, want 7", r.Generation, err)
	}

	if got := dirNames(t, dir); !slices.Equal(got, []string{ownerrecord.RecordFile, ownerrecord.LockFile}) {
		t.Errorf("a failed replace left %v", got)
	}

	// A failed create leaves no record at all.
	fresh := t.TempDir()
	c := ownerrecord.NewDirStore(fresh)
	ownerrecord.SetTempWriter(c, failing)

	if err := c.Replace(t.Context(), nil, liveAt(t, 7)); err == nil {
		t.Fatal("Replace reported success when its write failed")
	}

	if _, err := os.Stat(recordPath(fresh)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a failed create left a record file: %v", err)
	}

	if got := dirNames(t, fresh); !slices.Equal(got, []string{ownerrecord.LockFile}) {
		t.Errorf("a failed create left %v", got)
	}
}

func TestReadRefusesAStoredRecordThatDoesNotOpen(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s := ownerrecord.NewDirStore(dir)

	bad := ownerrecordtest.Fixture("record-bad-signature-v1.json")
	if err := os.WriteFile(recordPath(dir), bad, 0o640); err != nil {
		t.Fatal(err)
	}

	_, _, err := s.Read(t.Context())
	wantRefusal(t, err, progress.CodeRequestUnsigned)

	if !errors.Is(err, ownerrecord.ErrStoredRecordInvalid) {
		t.Errorf("Read of a record that does not open: err = %v, want ErrStoredRecordInvalid", err)
	}

	// A hand-over written by the instance handing ownership over does not
	// open, and Read returns its signed form so the caller can answer it as
	// a hand-over.
	handover := sealHandover(t)
	if err := os.WriteFile(recordPath(dir), encode(t, handover), 0o640); err != nil {
		t.Fatal(err)
	}

	sf, r, err := s.Read(t.Context())
	wantRefusal(t, err, progress.CodeRequestUnsigned)

	if !signedEqual(sf, handover) || r.InstallationID != "" {
		t.Errorf("Read returned %+v and %+v with its refusal, want the signed form only", sf, r)
	}

	wantRefusal(t, ownerrecord.HandoverRefusal(sf, err), progress.CodeOwnerHandover)

	// A stored file that is not a signed record at all.
	for name, data := range map[string][]byte{
		"not json":  []byte("not json"),
		"truncated": bad[:len(bad)/2],
		// A good record padded with white space past the size cap, which
		// would otherwise parse and open.
		"huge": append(encode(t, liveAt(t, 7)), bytes.Repeat([]byte(" "), 2<<20)...),
	} {
		if err := os.WriteFile(recordPath(dir), data, 0o640); err != nil {
			t.Fatal(err)
		}

		_, _, err := s.Read(t.Context())
		if r, ok := progress.AsRefusal(err); !ok || r.Code != progress.CodeJobRequestInvalid {
			t.Errorf("%s: err = %v, want a JOB_REQUEST_INVALID refusal", name, err)
		}

		if !errors.Is(err, ownerrecord.ErrStoredRecordInvalid) {
			t.Errorf("%s: err = %v, want ErrStoredRecordInvalid", name, err)
		}
	}
}

func TestReplaceRefusesAStoredRecordThatDoesNotOpen(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	s := ownerrecord.NewDirStore(dir)

	bad := ownerrecordtest.Fixture("record-bad-signature-v1.json")
	if err := os.WriteFile(recordPath(dir), bad, 0o640); err != nil {
		t.Fatal(err)
	}

	// The stored generation cannot be trusted, so nothing replaces it. The
	// error says that the stored record is at fault, and Open's refusal is
	// still underneath it.
	err := s.Replace(t.Context(), genPtr(7), liveAt(t, 8))
	if !errors.Is(err, ownerrecord.ErrStoredRecordInvalid) {
		t.Errorf("Replace over a stored record that does not open: err = %v, want ErrStoredRecordInvalid", err)
	}

	wantRefusal(t, err, progress.CodeRequestUnsigned)

	// A create-only write needs only to know that a record exists.
	wantRefusal(t, s.Replace(t.Context(), nil, liveAt(t, 8)), progress.CodeOwnerRecordExists)

	if !bytes.Equal(readFile(t, recordPath(dir)), bad) {
		t.Error("Replace changed a stored record that does not open")
	}

	// A stored file that is not a signed record at all is the stored
	// record's fault too.
	if err := os.WriteFile(recordPath(dir), []byte("not json"), 0o640); err != nil {
		t.Fatal(err)
	}

	err = s.Replace(t.Context(), genPtr(7), liveAt(t, 8))
	if !errors.Is(err, ownerrecord.ErrStoredRecordInvalid) {
		t.Errorf("Replace over a corrupt stored record: err = %v, want ErrStoredRecordInvalid", err)
	}

	wantRefusal(t, err, progress.CodeJobRequestInvalid)

	// A bad replacement over a sound stored record is not.
	good, _, _ := createdStore(t)

	unsignedNext := liveAt(t, 8)
	unsignedNext.Signature = bytes.Clone(unsignedNext.Signature)
	unsignedNext.Signature[0] ^= 0x01

	for _, next := range []ownerrecord.Signed{unsignedNext, liveAt(t, 9), otherOwnerAt(t, 8)} {
		if err := good.Replace(t.Context(), genPtr(7), next); err == nil || errors.Is(err, ownerrecord.ErrStoredRecordInvalid) {
			t.Errorf("a bad replacement over a sound record: err = %v, want a refusal without ErrStoredRecordInvalid", err)
		}
	}
}

func TestStoreHonoursACancelledContext(t *testing.T) {
	t.Parallel()

	s, _, _ := createdStore(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, _, err := s.Read(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Read: err = %v, want context.Canceled", err)
	}

	if _, err := s.RLock(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("RLock: err = %v, want context.Canceled", err)
	}

	if err := s.Replace(ctx, genPtr(7), liveAt(t, 8)); !errors.Is(err, context.Canceled) {
		t.Errorf("Replace: err = %v, want context.Canceled", err)
	}
}

func TestStoreRefusesAnEmptyDirectory(t *testing.T) {
	t.Parallel()

	s := ownerrecord.NewDirStore("")

	if _, _, err := s.Read(t.Context()); err == nil || errors.Is(err, ownerrecord.ErrNoRecord) {
		t.Errorf("Read with no directory: err = %v, want an error other than ErrNoRecord", err)
	}

	if err := s.Replace(t.Context(), nil, liveAt(t, 7)); err == nil {
		t.Error("Replace with no directory succeeded")
	}
}

func mustParse(t *testing.T, b []byte) ownerrecord.Signed {
	t.Helper()

	sf, err := ownerrecord.Parse(b)
	if err != nil {
		t.Fatal(err)
	}

	return sf
}

// tryExclusive reports whether a non-blocking exclusive lock on path is
// granted, and gives it straight back when it is.
func tryExclusive(t *testing.T, path string) bool {
	t.Helper()

	release, ok, err := ownerrecord.TryLockExclusive(path)
	if err != nil {
		t.Fatal(err)
	}

	if ok {
		if err := release(); err != nil {
			t.Fatal(err)
		}
	}

	return ok
}

func TestLockPathMustBeARegularFile(t *testing.T) {
	t.Parallel()

	// Each case plants something other than a regular file at the lock
	// path. The dangling link comes last because, without the check, it
	// would send the store round an endless loop.
	cases := []struct {
		name  string
		plant func(dir, lockPath string) error
	}{
		{"directory", func(_, lockPath string) error { return os.Mkdir(lockPath, 0o750) }},
		{"link to a regular file", func(dir, lockPath string) error {
			target := filepath.Join(dir, "elsewhere")
			if err := os.WriteFile(target, nil, 0o640); err != nil {
				return err
			}

			return os.Symlink(target, lockPath)
		}},
		{"dangling link", func(dir, lockPath string) error {
			return os.Symlink(filepath.Join(dir, "nowhere"), lockPath)
		}},
	}

	for _, c := range cases {
		dir := t.TempDir()
		lockPath := filepath.Join(dir, ownerrecord.LockFile)

		if err := c.plant(dir, lockPath); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("%s: %v", c.name, err)
			}

			t.Fatal(err)
		}

		s := ownerrecord.NewDirStore(dir)

		_, _, err := s.Read(t.Context())
		if r, ok := progress.AsRefusal(err); !ok || r.Code != progress.CodeJobRequestInvalid {
			t.Errorf("%s: Read err = %v, want a JOB_REQUEST_INVALID refusal", c.name, err)
		}

		if release, err := s.RLock(t.Context()); err == nil {
			_ = release()

			t.Errorf("%s: RLock took a lock", c.name)
		}

		if err := s.Replace(t.Context(), nil, liveAt(t, 7)); err == nil {
			t.Errorf("%s: Replace succeeded", c.name)
		}

		if _, err := os.Stat(recordPath(dir)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: a record was written: %v", c.name, err)
		}

		if _, err := os.Stat(filepath.Join(dir, "nowhere")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: the link's target was created: %v", c.name, err)
		}
	}
}

func TestReplaceReleasesTheLockAfterAPanic(t *testing.T) {
	t.Parallel()

	s, dir, stored := createdStore(t)
	ownerrecord.SetTempWriter(s, func(*os.File, []byte) (int, error) { panic("write failed hard") })

	recovered := func() (v any) {
		defer func() { v = recover() }()

		_ = s.Replace(t.Context(), genPtr(7), liveAt(t, 8))

		return nil
	}()

	if recovered == nil {
		t.Fatal("the temporary writer's panic did not reach the caller")
	}

	if ok := tryExclusive(t, filepath.Join(dir, ownerrecord.LockFile)); !ok {
		t.Error("the lock file is still locked after a recovered panic in Replace")
	}

	if !bytes.Equal(readFile(t, recordPath(dir)), stored) {
		t.Error("a panic in Replace changed the record")
	}

	if got := dirNames(t, dir); !slices.Equal(got, []string{ownerrecord.RecordFile, ownerrecord.LockFile}) {
		t.Errorf("a panic in Replace left %v", got)
	}
}

// oversizedAt is a record at generation gen that opens but whose stored form
// is larger than the store reads back.
func oversizedAt(t *testing.T, gen uint64) ownerrecord.Signed {
	t.Helper()

	r := liveRecord()
	r.Generation = gen
	r.HeadHash = strings.Repeat("a", 1<<20)

	return seal(t, r)
}

func TestReplaceRefusesAnOversizedRecord(t *testing.T) {
	t.Parallel()

	// A create is refused before the directory is even made.
	dir := filepath.Join(t.TempDir(), "owner")

	err := ownerrecord.NewDirStore(dir).Replace(t.Context(), nil, oversizedAt(t, 1))
	wantRefusal(t, err, progress.CodeJobRequestInvalid)

	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused create made the directory: %v", err)
	}

	// A replace is refused and the stored record stays as it was, so the
	// store can still read and replace it.
	s, dir, stored := createdStore(t)

	wantRefusal(t, s.Replace(t.Context(), genPtr(7), oversizedAt(t, 8)), progress.CodeJobRequestInvalid)

	if !bytes.Equal(readFile(t, recordPath(dir)), stored) {
		t.Error("a refused oversized replace changed the record")
	}

	if err := s.Replace(t.Context(), genPtr(7), liveAt(t, 8)); err != nil {
		t.Errorf("Replace after a refused oversized one: %v", err)
	}
}

func TestReplaceLetsOneOfManyWritersWin(t *testing.T) {
	t.Parallel()

	const rounds, writers = 20, 16

	for round := range rounds {
		_, dir, _ := createdStore(t)
		next := liveAt(t, 8)
		errs := make(chan error, writers)

		var wg sync.WaitGroup

		for range writers {
			wg.Go(func() {
				// Each writer has its own store, so they share only the disk.
				errs <- ownerrecord.NewDirStore(dir).Replace(context.Background(), genPtr(7), next)
			})
		}

		wg.Wait()
		close(errs)

		wins, changed := 0, 0

		for err := range errs {
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ownerrecord.ErrRecordChanged):
				changed++
			default:
				t.Errorf("round %d: unexpected error %v", round, err)
			}
		}

		if wins != 1 || changed != writers-1 {
			t.Fatalf("round %d: %d writers won and %d saw a change, want 1 and %d", round, wins, changed, writers-1)
		}
	}
}

func TestCreateLetsOneOfManyWritersWin(t *testing.T) {
	t.Parallel()

	const rounds, writers = 20, 16

	for round := range rounds {
		dir := filepath.Join(t.TempDir(), "owner")
		next := liveAt(t, 1)
		errs := make(chan error, writers)

		var wg sync.WaitGroup

		for range writers {
			wg.Go(func() {
				errs <- ownerrecord.NewDirStore(dir).Replace(context.Background(), nil, next)
			})
		}

		wg.Wait()
		close(errs)

		wins, exists := 0, 0

		for err := range errs {
			if err == nil {
				wins++

				continue
			}

			if r, ok := progress.AsRefusal(err); ok && r.Code == progress.CodeOwnerRecordExists {
				exists++

				continue
			}

			t.Errorf("round %d: unexpected error %v", round, err)
		}

		if wins != 1 || exists != writers-1 {
			t.Fatalf("round %d: %d creators won and %d were refused, want 1 and %d", round, wins, exists, writers-1)
		}
	}
}

func TestReplaceSyncsTheNewDirectorysParent(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	dir := filepath.Join(parent, "owner")
	s := ownerrecord.NewDirStore(dir)

	var synced []string

	ownerrecord.SetDirSyncer(s, func(d string) error {
		synced = append(synced, d)

		return ownerrecord.SyncDir(d)
	})

	if err := s.Replace(t.Context(), nil, liveAt(t, 7)); err != nil {
		t.Fatal(err)
	}

	// The first write makes the directory, so the parent's entry for it is
	// flushed before the record's rename is.
	if want := []string{parent, dir}; !slices.Equal(synced, want) {
		t.Errorf("the first write synced %v, want %v", synced, want)
	}

	synced = nil

	if err := s.Replace(t.Context(), genPtr(7), liveAt(t, 8)); err != nil {
		t.Fatal(err)
	}

	if want := []string{dir}; !slices.Equal(synced, want) {
		t.Errorf("a later write synced %v, want %v", synced, want)
	}
}

func TestReplaceSweepsStaleTemporaryFiles(t *testing.T) {
	t.Parallel()

	s, dir, _ := createdStore(t)

	stale := []string{".cosmos-owner.json.tmp-1", ".cosmos-owner.json.tmp-3141592653"}
	kept := []string{
		".cosmos-owner.json.tmp-",
		".cosmos-owner.json.tmp-12a",
		".cosmos-owner.json.tmp-12.bak",
		"cosmos-owner.json.tmp-12",
		".other.json.tmp-12",
		"notes.txt",
	}

	for _, name := range append(slices.Clone(stale), kept...) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
	}

	// A directory that matches the pattern is not a file the store wrote.
	keptDir := ".cosmos-owner.json.tmp-77"
	if err := os.Mkdir(filepath.Join(dir, keptDir), 0o750); err != nil {
		t.Fatal(err)
	}

	// A refused replace still holds the exclusive lock, so it sweeps too.
	if err := s.Replace(t.Context(), genPtr(6), liveAt(t, 7)); !errors.Is(err, ownerrecord.ErrRecordChanged) {
		t.Fatalf("err = %v, want ErrRecordChanged", err)
	}

	want := append([]string{ownerrecord.RecordFile, ownerrecord.LockFile, keptDir}, kept...)
	slices.Sort(want)

	if got := dirNames(t, dir); !slices.Equal(got, want) {
		t.Errorf("after the sweep the directory holds\n%v\nwant\n%v", got, want)
	}
}
