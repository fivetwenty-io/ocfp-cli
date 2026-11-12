//go:build unix

package commands

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord/ownerrecordtest"
)

// defaultHost is a test host whose default owner directory is def. The
// directory above def is data, and home is a private directory for links.
type defaultHost struct {
	base, data, def, home string
}

// newDefaultHost makes a host whose default owner directory is
// base/data/ocfp-owner. It makes data only when withData is true, and the
// default directory, holding testOwnerRecord, only when withDefault is true.
func newDefaultHost(t *testing.T, withData, withDefault bool) defaultHost {
	t.Helper()

	base := t.TempDir()
	h := defaultHost{
		base: base,
		data: filepath.Join(base, "data"),
		def:  filepath.Join(base, "data", "ocfp-owner"),
		home: filepath.Join(base, "home"),
	}

	mustMkdir(t, h.home)

	if withData || withDefault {
		mustMkdir(t, h.data)
	}

	if withDefault {
		storeRecord(t, h.def)
	}

	return h
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()

	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()

	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// storeRecord stores testOwnerRecord in dir.
func storeRecord(t *testing.T, dir string) {
	t.Helper()

	sf, err := ownerrecord.Seal(testOwnerRecord(), testSign)
	if err != nil {
		t.Fatal(err)
	}

	if err := ownerrecord.NewDirStore(dir).Replace(t.Context(), nil, sf); err != nil {
		t.Fatal(err)
	}
}

// writeWithDefault runs owner-record write, with def as the default owner
// directory, on a fresh record, adding args after the subcommand.
func writeWithDefault(t *testing.T, def string, args ...string) cmdResult {
	t.Helper()

	cmd := newOwnerRecordCmd(ownerrecord.NewDirStore, def)

	return runCmd(t, cmd, bytes.NewReader(sealedOwner(t, testOwnerRecord())), append([]string{"write"}, args...)...)
}

// wantDefaultRefused checks that res is the refusal of flag for reaching the
// default directory.
func wantDefaultRefused(t *testing.T, res cmdResult, flag string) {
	t.Helper()

	if res.code != 1 {
		t.Fatalf("exit code = %d (%v), stderr %q; want 1", res.code, res.err, res.stderr)
	}

	if !strings.Contains(res.stderr, flag) || !strings.Contains(res.stderr, "default directory") {
		t.Fatalf("stderr %q does not refuse %s for reaching the default directory", res.stderr, flag)
	}
}

func wantNoRecord(t *testing.T, dir string) {
	t.Helper()

	if _, err := os.Lstat(filepath.Join(dir, ownerrecord.RecordFile)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a record reached %s: %v", dir, err)
	}
}

// A directory flag that is not the default as written, but that leads to the
// default directory or below it, is refused before anything is read or
// written, whether or not the default directory exists yet.
func TestOwnerRecordRefusesADirThatReachesTheDefault(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                  string
		withData, withDefault bool
		// arrange returns the --dir value and the directory that must not
		// gain a record.
		arrange func(t *testing.T, h defaultHost) (string, string)
	}{
		{"a link to the default's parent before first use", true, false, func(t *testing.T, h defaultHost) (string, string) {
			mustSymlink(t, h.data, filepath.Join(h.home, "scratch"))

			return filepath.Join(h.home, "scratch", "ocfp-owner"), h.def
		}},
		{"a link to the default's parent", true, true, func(t *testing.T, h defaultHost) (string, string) {
			mustSymlink(t, h.data, filepath.Join(h.home, "scratch"))

			return filepath.Join(h.home, "scratch", "ocfp-owner"), ""
		}},
		{"a path under the default reached through a link", true, true, func(t *testing.T, h defaultHost) (string, string) {
			mustSymlink(t, h.def, filepath.Join(h.home, "into"))

			return filepath.Join(h.home, "into", "sub"), filepath.Join(h.def, "sub")
		}},
		{"a path under the default as written", true, true, func(_ *testing.T, h defaultHost) (string, string) {
			return filepath.Join(h.def, "sub"), filepath.Join(h.def, "sub")
		}},
		{"the default's parent not made yet", false, false, func(t *testing.T, h defaultHost) (string, string) {
			mustSymlink(t, h.base, filepath.Join(h.home, "up"))

			return filepath.Join(h.home, "up", "data", "ocfp-owner"), h.def
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newDefaultHost(t, tc.withData, tc.withDefault)

			var before []byte

			if tc.withDefault {
				var err error

				before, err = os.ReadFile(filepath.Join(h.def, ownerrecord.RecordFile))
				if err != nil {
					t.Fatal(err)
				}
			}

			dir, untouched := tc.arrange(t, h)

			wantDefaultRefused(t, writeWithDefault(t, h.def, "--dir", dir), "--dir")

			release := newOwnerRecordCmd(ownerrecord.NewDirStore, h.def)
			res := runCmd(t, release, bytes.NewReader(releaseProof(t, testOwnerRecord())),
				"release", "--dir", dir, "--expect-generation", "1")
			wantDefaultRefused(t, res, "--dir")

			if untouched != "" {
				wantNoRecord(t, untouched)
			}

			if tc.withDefault {
				after, err := os.ReadFile(filepath.Join(h.def, ownerrecord.RecordFile))
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("a refused command changed the default record: %v", err)
				}
			}
		})
	}
}

// The default itself, a directory outside it, and a directory whose name
// only starts like the default's all keep working.
func TestOwnerRecordAcceptsTheDefaultAndOtherDirs(t *testing.T) {
	t.Parallel()

	t.Run("the default as written", func(t *testing.T) {
		t.Parallel()

		h := newDefaultHost(t, true, false)

		if res := writeWithDefault(t, h.def, "--dir", h.def); res.code != 0 {
			t.Fatalf("write to the default = %d, %v, stderr %q", res.code, res.err, res.stderr)
		}

		storedIn(t, h.def)
	})

	t.Run("the default without the flag", func(t *testing.T) {
		t.Parallel()

		h := newDefaultHost(t, true, false)

		if res := writeWithDefault(t, h.def); res.code != 0 {
			t.Fatalf("write with no --dir = %d, %v, stderr %q", res.code, res.err, res.stderr)
		}

		storedIn(t, h.def)
	})

	// An operator's home or data directory can sit on a data disk behind a
	// link, and the default written as configured must still work there.
	t.Run("the default behind a link", func(t *testing.T) {
		t.Parallel()

		base := t.TempDir()
		disk := filepath.Join(base, "disk")
		mustMkdir(t, disk)
		mustSymlink(t, disk, filepath.Join(base, "data"))

		def := filepath.Join(base, "data", "ocfp-owner")

		if res := writeWithDefault(t, def, "--dir", def); res.code != 0 {
			t.Fatalf("write to a linked default = %d, %v, stderr %q", res.code, res.err, res.stderr)
		}

		storedIn(t, filepath.Join(disk, "ocfp-owner"))

		// The same directory named by where the link leads is not the
		// default as written.
		other := newOwnerRecordCmd(ownerrecord.NewDirStore, def)
		res := runCmd(t, other, bytes.NewReader(releaseProof(t, testOwnerRecord())),
			"release", "--dir", filepath.Join(disk, "ocfp-owner"), "--expect-generation", "1")
		wantDefaultRefused(t, res, "--dir")
	})

	for _, tc := range []struct {
		name                  string
		withData, withDefault bool
		dir                   func(h defaultHost) string
	}{
		{"an unrelated directory", true, true, func(h defaultHost) string { return filepath.Join(h.home, "owner") }},
		{"a sibling whose name starts like the default", true, true, func(h defaultHost) string { return h.def + "-dev" }},
		{"an unrelated directory before the default exists", false, false, func(h defaultHost) string {
			return filepath.Join(h.home, "owner")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newDefaultHost(t, tc.withData, tc.withDefault)
			dir := tc.dir(h)

			if res := writeWithDefault(t, h.def, "--dir", dir); res.code != 0 {
				t.Fatalf("write to %s = %d, %v, stderr %q", dir, res.code, res.err, res.stderr)
			}

			storedIn(t, dir)
		})
	}
}

func storedIn(t *testing.T, dir string) {
	t.Helper()

	if _, err := os.Stat(filepath.Join(dir, ownerrecord.RecordFile)); err != nil {
		t.Fatalf("no record in %s: %v", dir, err)
	}
}

// job exec applies the same rule to --owner-dir, before it reads the request
// or makes a spool.
func TestJobExecRefusesAnOwnerDirThatReachesTheDefault(t *testing.T) {
	t.Parallel()

	h := newDefaultHost(t, true, true)
	mustSymlink(t, h.data, filepath.Join(h.home, "scratch"))

	for _, dir := range []string{
		filepath.Join(h.home, "scratch", "ocfp-owner"),
		filepath.Join(h.def, "sub"),
	} {
		spool := filepath.Join(t.TempDir(), "spool")

		res := runCmd(t, newJobCmd(ownerrecord.NewDirStore, h.def),
			bytes.NewReader(jobRequest(t, "job-default", ownerrecordtest.TestKey())),
			"exec", "--spool-root", spool, "--owner-dir", dir)
		wantDefaultRefused(t, res, "--owner-dir")

		if res.stdout != "" {
			t.Fatalf("a refused flag wrote a stream %q", res.stdout)
		}

		if _, err := os.Lstat(spool); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("a refused flag made a spool root: %v", err)
		}
	}

	spool := filepath.Join(t.TempDir(), "spool")

	res := runCmd(t, newJobCmd(ownerrecord.NewDirStore, h.def),
		bytes.NewReader(jobRequest(t, "job-default", ownerrecordtest.TestKey())),
		"exec", "--spool-root", spool, "--owner-dir", h.def)
	if res.code != 0 {
		t.Fatalf("job exec with the default owner dir = %d, %v, stderr %q", res.code, res.err, res.stderr)
	}
}

// runJobExec runs job exec with def as the default owner directory, adding
// args after the subcommand.
func runJobExec(t *testing.T, def string, args ...string) cmdResult {
	t.Helper()

	return runCmd(t, newJobCmd(ownerrecord.NewDirStore, def),
		bytes.NewReader(jobRequest(t, "job-spool", ownerrecordtest.TestKey())),
		append([]string{"exec"}, args...)...)
}

// A spool root that resolves to the owner directory in use, or to a
// directory under it, is refused before the request is read, whatever path
// leads there. The owner directory in use is --owner-dir when it is given and
// the default otherwise.
func TestJobExecRefusesASpoolRootInTheOwnerDir(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                  string
		withData, withDefault bool
		// arrange returns the --spool-root value, the other arguments, and
		// the directory that must not appear.
		arrange func(t *testing.T, h defaultHost) (string, []string, string)
	}{
		{"under the owner default through a link", true, true, func(t *testing.T, h defaultHost) (string, []string, string) {
			mustSymlink(t, h.def, filepath.Join(h.home, "jobs"))

			return filepath.Join(h.home, "jobs", "spool"), nil, filepath.Join(h.def, "spool")
		}},
		{"under the owner default as written", true, true, func(_ *testing.T, h defaultHost) (string, []string, string) {
			return filepath.Join(h.def, "spool"), nil, filepath.Join(h.def, "spool")
		}},
		{"the owner default itself", true, true, func(_ *testing.T, h defaultHost) (string, []string, string) {
			return h.def, nil, filepath.Join(h.def, "job-spool")
		}},
		{"under the owner default before it exists", true, false, func(t *testing.T, h defaultHost) (string, []string, string) {
			mustSymlink(t, h.data, filepath.Join(h.home, "scratch"))

			return filepath.Join(h.home, "scratch", "ocfp-owner", "spool"), nil, h.def
		}},
		{"under a custom owner dir", true, false, func(t *testing.T, h defaultHost) (string, []string, string) {
			owner := filepath.Join(h.home, "owner")
			storeRecord(t, owner)

			return filepath.Join(owner, "spool"), []string{"--owner-dir", owner}, filepath.Join(owner, "spool")
		}},
		{"under a custom owner dir through a link", true, false, func(t *testing.T, h defaultHost) (string, []string, string) {
			owner := filepath.Join(h.home, "owner")
			storeRecord(t, owner)
			mustSymlink(t, owner, filepath.Join(h.home, "jobs"))

			return filepath.Join(h.home, "jobs", "spool"), []string{"--owner-dir", owner}, filepath.Join(owner, "spool")
		}},
		{"holding a custom owner dir", true, false, func(t *testing.T, h defaultHost) (string, []string, string) {
			owner := filepath.Join(h.home, "owner")
			storeRecord(t, owner)

			return h.home, []string{"--owner-dir", owner}, filepath.Join(h.home, "job-spool")
		}},
		{"holding the owner default", true, true, func(_ *testing.T, h defaultHost) (string, []string, string) {
			return h.data, nil, filepath.Join(h.data, "job-spool")
		}},
		{"holding the owner default through a link", true, true, func(t *testing.T, h defaultHost) (string, []string, string) {
			mustSymlink(t, h.data, filepath.Join(h.home, "up"))

			return filepath.Join(h.home, "up"), nil, filepath.Join(h.data, "job-spool")
		}},
		{"under the owner default with a custom owner dir", true, true, func(t *testing.T, h defaultHost) (string, []string, string) {
			owner := filepath.Join(h.home, "owner")
			storeRecord(t, owner)

			return filepath.Join(h.def, "jobs"), []string{"--owner-dir", owner}, filepath.Join(h.def, "jobs")
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newDefaultHost(t, tc.withData, tc.withDefault)
			spool, args, absent := tc.arrange(t, h)

			res := runJobExec(t, h.def, append([]string{"--spool-root", spool}, args...)...)
			if res.code != 1 {
				t.Fatalf("exit code = %d (%v), stderr %q; want 1", res.code, res.err, res.stderr)
			}

			if !strings.Contains(res.stderr, "--spool-root") || !strings.Contains(res.stderr, "owner directory") {
				t.Fatalf("stderr %q does not refuse --spool-root for resolving into the owner directory", res.stderr)
			}

			if res.stdout != "" {
				t.Fatalf("a refused flag wrote a stream %q", res.stdout)
			}

			if _, err := os.Lstat(absent); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("a refused flag made %s: %v", absent, err)
			}
		})
	}
}

// With a custom owner directory in use, a spool root that holds the default
// owner directory is refused too, so that a job whose id is the default's
// last name can never make the default its spool.
func TestJobExecRefusesASpoolRootHoldingTheDefault(t *testing.T) {
	t.Parallel()

	for _, withDefault := range []bool{false, true} {
		name := "before the default exists"
		if withDefault {
			name = "with the default made"
		}

		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newDefaultHost(t, true, withDefault)

			owner := filepath.Join(h.home, "owner")
			storeRecord(t, owner)

			var before []os.DirEntry

			if withDefault {
				var err error

				before, err = os.ReadDir(h.def)
				if err != nil {
					t.Fatal(err)
				}
			}

			res := runCmd(t, newJobCmd(ownerrecord.NewDirStore, h.def),
				bytes.NewReader(jobRequest(t, filepath.Base(h.def), ownerrecordtest.TestKey())),
				"exec", "--spool-root", h.data, "--owner-dir", owner)
			if res.code != 1 || !strings.Contains(res.stderr, "--spool-root") || !strings.Contains(res.stderr, "default owner directory") {
				t.Fatalf("exit code = %d, stderr %q; want --spool-root refused for holding the default owner directory", res.code, res.stderr)
			}

			if res.stdout != "" {
				t.Fatalf("a refused flag wrote a stream %q", res.stdout)
			}

			after, err := os.ReadDir(h.def)

			switch {
			case !withDefault && !errors.Is(err, fs.ErrNotExist):
				t.Fatalf("a refused spool root made the default owner directory: %v", err)
			case withDefault && (err != nil || len(after) != len(before)):
				t.Fatalf("a refused spool root changed the default owner directory: %d entries, was %d, %v", len(after), len(before), err)
			}
		})
	}
}

// A spool root outside the owner directory in use keeps working, including
// one beside it whose name starts the same way.
func TestJobExecAcceptsASpoolRootOutsideTheOwnerDir(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		arrange func(t *testing.T, h defaultHost) (string, []string)
	}{
		{"an unrelated spool root", func(t *testing.T, _ defaultHost) (string, []string) {
			return filepath.Join(t.TempDir(), "spool"), nil
		}},
		{"a sibling of the owner default", func(_ *testing.T, h defaultHost) (string, []string) {
			return h.def + "-jobs", nil
		}},
		{"under the default with a custom owner dir", func(t *testing.T, h defaultHost) (string, []string) {
			owner := filepath.Join(h.home, "owner")
			storeRecord(t, owner)

			return filepath.Join(h.data, "jobs"), []string{"--owner-dir", owner}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h := newDefaultHost(t, true, true)
			spool, args := tc.arrange(t, h)

			res := runJobExec(t, h.def, append([]string{"--spool-root", spool}, args...)...)
			if res.code != 0 {
				t.Fatalf("job exec with spool root %s = %d, %v, stderr %q", spool, res.code, res.err, res.stderr)
			}

			if _, err := os.Stat(filepath.Join(spool, "job-spool")); err != nil {
				t.Fatalf("no spool for the job under %s: %v", spool, err)
			}
		})
	}
}

// caseInsensitive reports whether the file system that holds dir finds a
// name whatever its case.
func caseInsensitive(t *testing.T, dir string) bool {
	t.Helper()

	probe := filepath.Join(dir, "case-probe")
	mustMkdir(t, probe)

	_, err := os.Stat(filepath.Join(dir, "CASE-PROBE"))

	return err == nil
}

// A second name for the default directory that is not a symbolic link, such
// as a different case on a file system that ignores case, or a firmlink, is
// refused like a link.
func TestDirFlagsRefuseAnAliasOfTheDefault(t *testing.T) {
	t.Parallel()

	t.Run("a case variant before first use", func(t *testing.T) {
		t.Parallel()

		h := newDefaultHost(t, true, false)
		if !caseInsensitive(t, h.home) {
			t.Skip("this file system tells names apart by case")
		}

		wantDefaultRefused(t, writeWithDefault(t, h.def, "--dir", filepath.Join(h.base, "DATA", "ocfp-owner")), "--dir")
		wantNoRecord(t, h.def)
	})

	t.Run("a link whose target differs only in case", func(t *testing.T) {
		t.Parallel()

		h := newDefaultHost(t, true, false)
		if !caseInsensitive(t, h.home) {
			t.Skip("this file system tells names apart by case")
		}

		mustSymlink(t, filepath.Join(h.base, "DATA"), filepath.Join(h.home, "scratch"))

		wantDefaultRefused(t, writeWithDefault(t, h.def, "--dir", filepath.Join(h.home, "scratch", "OCFP-OWNER")), "--dir")
		wantNoRecord(t, h.def)
	})

	t.Run("a spool root under a case variant of the default", func(t *testing.T) {
		t.Parallel()

		h := newDefaultHost(t, true, true)
		if !caseInsensitive(t, h.home) {
			t.Skip("this file system tells names apart by case")
		}

		res := runJobExec(t, h.def, "--spool-root", filepath.Join(h.base, "Data", "ocfp-owner", "jobs"))
		if res.code != 1 || !strings.Contains(res.stderr, "--spool-root") {
			t.Fatalf("job exec = %d, stderr %q; want --spool-root refused with exit 1", res.code, res.stderr)
		}

		if _, err := os.Lstat(filepath.Join(h.def, "jobs")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("a refused spool root was made in the owner directory: %v", err)
		}
	})

	t.Run("a firmlink spelling", func(t *testing.T) {
		t.Parallel()

		h := newDefaultHost(t, true, false)

		data, err := filepath.EvalSymlinks(h.data)
		if err != nil {
			t.Fatal(err)
		}

		alias := filepath.Join("/System/Volumes/Data", data)

		aliasInfo, err := os.Stat(alias)
		if err != nil {
			t.Skipf("no firmlink spelling here: %v", err)
		}

		dataInfo, err := os.Stat(data)
		if err != nil || !os.SameFile(aliasInfo, dataInfo) {
			t.Skipf("%s is not the same directory as %s", alias, data)
		}

		wantDefaultRefused(t, writeWithDefault(t, h.def, "--dir", filepath.Join(alias, "ocfp-owner")), "--dir")
		wantNoRecord(t, h.def)
	})
}

// The directory above the default is not inside it.
func TestOwnerRecordAcceptsTheDefaultsParent(t *testing.T) {
	t.Parallel()

	h := newDefaultHost(t, true, true)

	if res := writeWithDefault(t, h.def, "--dir", h.data); res.code != 0 {
		t.Fatalf("write to the default's parent = %d, %v, stderr %q", res.code, res.err, res.stderr)
	}

	storedIn(t, h.data)
}

// A flag value that is, or passes through, a symbolic link to a directory
// that does not exist yet is refused, wherever the link leads.
func TestDirFlagsRefuseALinkToAMissingDirectory(t *testing.T) {
	t.Parallel()

	wantDangling := func(t *testing.T, res cmdResult, flag string) {
		t.Helper()

		if res.code != 1 || !strings.Contains(res.stderr, flag) || !strings.Contains(res.stderr, "does not exist yet") {
			t.Fatalf("exit code = %d, stderr %q; want %s refused for a link to a missing directory", res.code, res.stderr, flag)
		}
	}

	t.Run("a link to the default's parent", func(t *testing.T) {
		t.Parallel()

		h := newDefaultHost(t, false, false)
		mustSymlink(t, h.data, filepath.Join(h.home, "scratch"))

		wantDangling(t, writeWithDefault(t, h.def, "--dir", filepath.Join(h.home, "scratch", "ocfp-owner")), "--dir")
	})

	t.Run("a link to the default at the last name", func(t *testing.T) {
		t.Parallel()

		h := newDefaultHost(t, true, false)
		mustSymlink(t, h.def, filepath.Join(h.home, "owner"))

		wantDangling(t, writeWithDefault(t, h.def, "--dir", filepath.Join(h.home, "owner")), "--dir")
	})

	t.Run("a link to an unrelated directory", func(t *testing.T) {
		t.Parallel()

		h := newDefaultHost(t, true, true)
		mustSymlink(t, filepath.Join(h.base, "elsewhere"), filepath.Join(h.home, "owner"))

		wantDangling(t, writeWithDefault(t, h.def, "--dir", filepath.Join(h.home, "owner")), "--dir")
	})

	t.Run("an owner dir", func(t *testing.T) {
		t.Parallel()

		h := newDefaultHost(t, true, true)
		mustSymlink(t, filepath.Join(h.base, "elsewhere"), filepath.Join(h.home, "owner"))

		res := runJobExec(t, h.def, "--spool-root", filepath.Join(t.TempDir(), "spool"),
			"--owner-dir", filepath.Join(h.home, "owner"))
		wantDangling(t, res, "--owner-dir")
	})

	t.Run("a spool root", func(t *testing.T) {
		t.Parallel()

		h := newDefaultHost(t, true, false)
		mustSymlink(t, filepath.Join(h.def, "jobs"), filepath.Join(h.home, "jobs"))

		wantDangling(t, runJobExec(t, h.def, "--spool-root", filepath.Join(h.home, "jobs")), "--spool-root")

		if _, err := os.Lstat(h.def); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("a refused spool root made the owner directory: %v", err)
		}
	})
}

// When the default or the owner directory in use fails its own check, the
// command refuses and names that directory, rather than letting every value
// through.
func TestDirFlagsRefuseWhenTheOwnerDirectoryFailsItsCheck(t *testing.T) {
	t.Parallel()

	wantUnchecked := func(t *testing.T, res cmdResult, dir string) {
		t.Helper()

		if res.code != 1 || !strings.Contains(res.stderr, dir) || !strings.Contains(res.stderr, "cannot be checked") {
			t.Fatalf("exit code = %d, stderr %q; want a refusal naming %s", res.code, res.stderr, dir)
		}
	}

	// open makes dir writable by every user, which the check refuses above a
	// store.
	open := func(t *testing.T, dir string) {
		t.Helper()

		if err := os.Chmod(dir, 0o777); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("the default, for --dir", func(t *testing.T) {
		t.Parallel()

		h := newDefaultHost(t, true, true)
		open(t, h.data)

		wantUnchecked(t, writeWithDefault(t, h.def, "--dir", filepath.Join(h.home, "owner")), h.def)
		wantNoRecord(t, filepath.Join(h.home, "owner"))
	})

	t.Run("the default, for --owner-dir", func(t *testing.T) {
		t.Parallel()

		h := newDefaultHost(t, true, true)
		open(t, h.data)

		owner := filepath.Join(h.home, "owner")
		storeRecord(t, owner)

		res := runJobExec(t, h.def, "--spool-root", filepath.Join(t.TempDir(), "spool"), "--owner-dir", owner)
		wantUnchecked(t, res, h.def)
	})

	// The owner directory in use is checked by the owner store, which
	// refuses it with its own code before any spool is made. The spool rule
	// still refuses a spool root inside it.
	t.Run("a custom owner dir that fails its check", func(t *testing.T) {
		t.Parallel()

		h := newDefaultHost(t, true, true)

		parent := filepath.Join(h.home, "shared")
		mustMkdir(t, parent)

		owner := filepath.Join(parent, "owner")
		storeRecord(t, owner)
		open(t, parent)

		res := runJobExec(t, h.def, "--spool-root", filepath.Join(owner, "jobs"), "--owner-dir", owner)
		if res.code != 1 || !strings.Contains(res.stderr, "resolves into the owner directory") {
			t.Fatalf("exit code = %d, stderr %q; want --spool-root refused for resolving into the owner directory", res.code, res.stderr)
		}

		if _, err := os.Lstat(filepath.Join(owner, "jobs")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("a refused spool root was made in the owner directory: %v", err)
		}

		spool := filepath.Join(t.TempDir(), "spool")

		res = runJobExec(t, h.def, "--spool-root", spool, "--owner-dir", owner)
		if res.code != 2 || !strings.Contains(res.stderr, parent) {
			t.Fatalf("exit code = %d, stderr %q; want the store's refusal naming %s", res.code, res.stderr, parent)
		}

		if _, err := os.Lstat(spool); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("a refused command made a spool root: %v", err)
		}
	})

	t.Run("the default, for --spool-root with a custom owner dir", func(t *testing.T) {
		t.Parallel()

		h := newDefaultHost(t, true, true)

		owner := filepath.Join(h.home, "owner")
		storeRecord(t, owner)
		open(t, h.data)

		// --owner-dir is refused first, because it is compared with the
		// default too.
		res := runJobExec(t, h.def, "--spool-root", filepath.Join(t.TempDir(), "spool"), "--owner-dir", owner)
		wantUnchecked(t, res, h.def)
	})
}
