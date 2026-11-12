//go:build unix

package commands

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord/ownerrecordtest"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

func chmodT(t *testing.T, path string, mode fs.FileMode) {
	t.Helper()

	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// unsafeOwnerDirs are owner directories that another user could change. Each
// one holds testOwnerRecord, and each returns the path to pass as the
// directory.
var unsafeOwnerDirs = []struct {
	name    string
	arrange func(t *testing.T) string
}{
	{"a linked directory", func(t *testing.T) string {
		dir := ownerDirWith(t)
		link := filepath.Join(t.TempDir(), "owner-link")

		if err := os.Symlink(dir, link); err != nil {
			t.Fatal(err)
		}

		return link
	}},
	{"a linked record", func(t *testing.T) string {
		dir := ownerDirWith(t)
		path := filepath.Join(dir, ownerrecord.RecordFile)
		moved := filepath.Join(t.TempDir(), "record.json")

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(moved, data, 0o600); err != nil {
			t.Fatal(err)
		}

		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}

		if err := os.Symlink(moved, path); err != nil {
			t.Fatal(err)
		}

		return dir
	}},
	{"a group-writable directory", func(t *testing.T) string {
		dir := ownerDirWith(t)
		chmodT(t, dir, 0o770)

		return dir
	}},
	{"an other-writable directory", func(t *testing.T) string {
		dir := ownerDirWith(t)
		chmodT(t, dir, 0o757)

		return dir
	}},
	{"a writable parent", func(t *testing.T) string {
		dir := ownerDirWith(t)
		chmodT(t, filepath.Dir(dir), 0o777)

		return dir
	}},
}

func TestJobExecRefusesAnUnsafeOwnerDir(t *testing.T) {
	t.Parallel()

	for _, tc := range unsafeOwnerDirs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			spool := filepath.Join(t.TempDir(), "spool")

			res := runCmd(t, NewJobCmd(), bytes.NewReader(jobRequest(t, "job-unsafe", ownerrecordtest.TestKey())),
				"exec", "--spool-root", spool, "--owner-dir", tc.arrange(t))
			if res.code != 2 {
				t.Fatalf("job exec = %d, %v, want 2", res.code, res.err)
			}

			want := []string{"header", "error:" + string(progress.CodeOwnerRecordInvalid), "result"}
			if got := streamKinds(t, res.stdout); !slices.Equal(got, want) {
				t.Fatalf("stream kinds = %v, want %v", got, want)
			}

			if !strings.Contains(res.stderr, string(progress.CodeOwnerRecordInvalid)) {
				t.Fatalf("stderr %q does not name %s", res.stderr, progress.CodeOwnerRecordInvalid)
			}

			if _, err := os.Stat(filepath.Join(spool, "job-unsafe")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a refused job has a spool: %v", err)
			}
		})
	}
}

func TestOwnerRecordRefusesAnUnsafeDir(t *testing.T) {
	t.Parallel()

	next := testOwnerRecord()
	next.Generation = 2

	for _, tc := range unsafeOwnerDirs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := tc.arrange(t)
			path := filepath.Join(dir, ownerrecord.RecordFile)

			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			res := writeRecord(t, sealedOwner(t, next), "--dir", dir, "--expect-generation", "1")
			wantRefused(t, res, progress.CodeOwnerRecordInvalid)

			res = releaseRecord(t, releaseProof(t, testOwnerRecord()), "--dir", dir, "--expect-generation", "1")
			wantRefused(t, res, progress.CodeOwnerRecordInvalid)

			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("a refused command changed the record: %v", err)
			}
		})
	}
}

// Anyone can sign an owner record that names their own key, so whoever can
// write the owner directory could swap the record for one of their own and
// run whatever they sign. The command refuses the directory instead.
func TestJobExecRefusesATakeoverThroughAWritableOwnerDir(t *testing.T) {
	t.Parallel()

	dir := ownerDirWith(t)
	chmodT(t, dir, 0o777)

	path := filepath.Join(dir, ownerrecord.RecordFile)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	theirs := testOwnerRecord()
	theirs.OwnerPublicKey = otherKey.Public().(ed25519.PublicKey)

	sf, err := ownerrecord.Seal(theirs, func(msg []byte) ([]byte, error) {
		return ed25519.Sign(otherKey, msg), nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, jsonLine(t, sf), 0o600); err != nil {
		t.Fatal(err)
	}

	spool := filepath.Join(t.TempDir(), "spool")

	res := runCmd(t, NewJobCmd(), bytes.NewReader(jobRequest(t, "job-takeover", otherKey)),
		"exec", "--spool-root", spool, "--owner-dir", dir)
	if res.code != 2 {
		t.Fatalf("job exec = %d, %v, stderr %q; want 2", res.code, res.err, res.stderr)
	}

	if !strings.Contains(res.stderr, string(progress.CodeOwnerRecordInvalid)) {
		t.Fatalf("stderr %q does not name %s", res.stderr, progress.CodeOwnerRecordInvalid)
	}

	if _, err := os.Stat(filepath.Join(spool, "job-takeover")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the swapped-in owner's job ran: %v", err)
	}
}
