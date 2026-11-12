package commands

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// writeRecord runs owner-record write with stdin and args after the
// subcommand.
func writeRecord(t *testing.T, stdin []byte, args ...string) cmdResult {
	t.Helper()

	return runCmd(t, NewOwnerRecordCmd(), bytes.NewReader(stdin), append([]string{"write"}, args...)...)
}

// releaseRecord runs owner-record release with stdin and args after the
// subcommand.
func releaseRecord(t *testing.T, stdin []byte, args ...string) cmdResult {
	t.Helper()

	return runCmd(t, NewOwnerRecordCmd(), bytes.NewReader(stdin), append([]string{"release"}, args...)...)
}

// releaseProof is the owner's release proof for r, on stdin.
func releaseProof(t *testing.T, r ownerrecord.Record) []byte {
	t.Helper()

	sf, err := ownerrecord.SealRelease(r, testSign)
	if err != nil {
		t.Fatal(err)
	}

	return jsonLine(t, sf)
}

func wantRefused(t *testing.T, res cmdResult, code progress.Code) {
	t.Helper()

	if res.code != 2 {
		t.Fatalf("exit code = %d (%v), want 2 for %s", res.code, res.err, code)
	}

	if !strings.Contains(res.stderr, string(code)) {
		t.Fatalf("stderr %q does not name %s", res.stderr, code)
	}
}

func TestOwnerRecordFlagDefaults(t *testing.T) {
	t.Parallel()

	for _, sub := range []string{"write", "release"} {
		cmd, _, err := NewOwnerRecordCmd().Find([]string{sub})
		if err != nil {
			t.Fatal(err)
		}

		if def := cmd.Flags().Lookup("dir").DefValue; def != ownerrecord.DefaultDir {
			t.Errorf("%s --dir defaults to %q, want %q", sub, def, ownerrecord.DefaultDir)
		}
	}
}

func TestOwnerRecordWriteCreateOnly(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "owner")
	path := filepath.Join(dir, ownerrecord.RecordFile)

	res := writeRecord(t, sealedOwner(t, testOwnerRecord()), "--dir", dir)
	if res.code != 0 {
		t.Fatalf("first write = %d, %v, stderr %q", res.code, res.err, res.stderr)
	}

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the first write stored nothing: %v", err)
	}

	next := testOwnerRecord()
	next.Generation = 2

	res = writeRecord(t, sealedOwner(t, next), "--dir", dir)
	wantRefused(t, res, progress.CodeOwnerRecordExists)

	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("a refused write changed the record: %v", err)
	}
}

func TestOwnerRecordWriteNextGeneration(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "owner")

	if res := writeRecord(t, sealedOwner(t, testOwnerRecord()), "--dir", dir); res.code != 0 {
		t.Fatalf("first write = %d, %v", res.code, res.err)
	}

	next := testOwnerRecord()
	next.Generation = 2

	res := writeRecord(t, sealedOwner(t, next), "--dir", dir, "--expect-generation", "5")
	wantRefused(t, res, progress.CodeOwnerMismatch)

	if !errors.Is(res.err, ownerrecord.ErrRecordChanged) {
		t.Fatalf("err = %v, want ErrRecordChanged", res.err)
	}

	res = writeRecord(t, sealedOwner(t, next), "--dir", dir, "--expect-generation", "1")
	if res.code != 0 {
		t.Fatalf("write of generation 2 = %d, %v, stderr %q", res.code, res.err, res.stderr)
	}

	_, r, err := ownerrecord.NewDirStore(dir).Read(t.Context())
	if err != nil || r.Generation != 2 {
		t.Fatalf("stored record = generation %d, %v; want 2", r.Generation, err)
	}
}

func TestOwnerRecordWriteExpectingAMissingRecord(t *testing.T) {
	t.Parallel()

	res := writeRecord(t, sealedOwner(t, testOwnerRecord()), "--dir", filepath.Join(t.TempDir(), "owner"), "--expect-generation", "0")
	wantRefused(t, res, progress.CodeOwnerRecordMissing)
}

func TestOwnerRecordWriteRefusals(t *testing.T) {
	t.Parallel()

	bad := testOwnerRecord()

	badSigned, err := ownerrecord.Seal(bad, testSign)
	if err != nil {
		t.Fatal(err)
	}

	badSigned.Signature[0] ^= 0x01

	cases := []struct {
		name  string
		stdin []byte
		code  progress.Code
	}{
		{"not a signed record", []byte("{\"hello\":1}\n"), progress.CodeJobRequestInvalid},
		{"not json", []byte("owner\n"), progress.CodeJobRequestInvalid},
		{"broken signature", jsonLine(t, badSigned), progress.CodeRequestUnsigned},
		{"too large", bytes.Repeat([]byte(" "), 2<<20), progress.CodeJobRequestInvalid},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := filepath.Join(t.TempDir(), "owner")

			res := writeRecord(t, tc.stdin, "--dir", dir)
			wantRefused(t, res, tc.code)

			if _, err := os.Stat(filepath.Join(dir, ownerrecord.RecordFile)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a refused write stored a record: %v", err)
			}
		})
	}
}

// A stored record that does not open is reported as the host's fault and
// never as a fault in the record on stdin.
func TestOwnerRecordWriteOverAnInvalidRecord(t *testing.T) {
	t.Parallel()

	dir := badOwnerDir(t)

	next := testOwnerRecord()
	next.Generation = 2

	res := writeRecord(t, sealedOwner(t, next), "--dir", dir, "--expect-generation", "1")
	wantRefused(t, res, progress.CodeOwnerRecordInvalid)
}

func TestOwnerRecordFailures(t *testing.T) {
	t.Parallel()

	record := sealedOwner(t, testOwnerRecord())

	cases := []struct {
		name string
		run  func(t *testing.T) cmdResult
	}{
		{"write with a relative dir", func(t *testing.T) cmdResult {
			return writeRecord(t, record, "--dir", "owner")
		}},
		{"write with empty stdin", func(t *testing.T) cmdResult {
			return writeRecord(t, nil, "--dir", filepath.Join(t.TempDir(), "owner"))
		}},
		{"write with unreadable stdin", func(t *testing.T) cmdResult {
			return runCmd(t, NewOwnerRecordCmd(), errReader{}, "write", "--dir", filepath.Join(t.TempDir(), "owner"))
		}},
		{"release with a relative dir", func(t *testing.T) cmdResult {
			return releaseRecord(t, releaseProof(t, testOwnerRecord()), "--dir", "owner", "--expect-generation", "1")
		}},
		{"release with no generation", func(t *testing.T) cmdResult {
			return releaseRecord(t, releaseProof(t, testOwnerRecord()), "--dir", ownerDirWith(t))
		}},
		{"release with unreadable stdin", func(t *testing.T) cmdResult {
			return runCmd(t, NewOwnerRecordCmd(), errReader{}, "release", "--dir", ownerDirWith(t), "--expect-generation", "1")
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			res := tc.run(t)
			if res.code != 1 {
				t.Fatalf("exit code = %d (%v), want 1", res.code, res.err)
			}
		})
	}
}

func TestReleaseNeedsCurrentGeneration(t *testing.T) {
	t.Parallel()

	dir := ownerDirWith(t)
	path := filepath.Join(dir, ownerrecord.RecordFile)
	proof := releaseProof(t, testOwnerRecord())

	res := releaseRecord(t, proof, "--dir", dir, "--expect-generation", "2")
	wantRefused(t, res, progress.CodeOwnerMismatch)

	if !errors.Is(res.err, ownerrecord.ErrRecordChanged) {
		t.Fatalf("err = %v, want ErrRecordChanged", res.err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a stale release removed the record: %v", err)
	}

	res = releaseRecord(t, proof, "--dir", t.TempDir(), "--expect-generation", "1")
	wantRefused(t, res, progress.CodeOwnerRecordMissing)

	res = releaseRecord(t, proof, "--dir", dir, "--expect-generation", "1")
	if res.code != 0 {
		t.Fatalf("the owner's release = %d, %v, stderr %q", res.code, res.err, res.stderr)
	}

	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the record is still there after release: %v", err)
	}
}

func TestReleaseRefusesTheWriteForm(t *testing.T) {
	t.Parallel()

	dir := ownerDirWith(t)

	res := releaseRecord(t, sealedOwner(t, testOwnerRecord()), "--dir", dir, "--expect-generation", "1")
	wantRefused(t, res, progress.CodeRequestUnsigned)

	if _, err := os.Stat(filepath.Join(dir, ownerrecord.RecordFile)); err != nil {
		t.Fatalf("a refused release removed the record: %v", err)
	}
}
