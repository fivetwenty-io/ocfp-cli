//go:build unix

package jobexec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"golang.org/x/sys/unix"
)

func statID(t *testing.T, path string) fileID {
	t.Helper()

	var st unix.Stat_t

	must(t, unix.Stat(path, &st))

	return fileID{Dev: uint64(st.Dev), Ino: st.Ino} //nolint:gosec,unconvert // the types differ by system
}

// The verb process inherits neither out nor the owner record's lock file.
// flock is held per open file, so an inherited copy of either would keep a
// job looking live, or the owner record locked, after the wrapper had gone.
func TestVerbChildInheritsNoLock(t *testing.T) {
	t.Parallel()

	ownerDir := filepath.Join(t.TempDir(), "owner")
	store := ownerrecord.NewDirStore(ownerDir)
	must(t, store.Replace(t.Context(), nil, sealRecord(t, liveRecord())))

	root := spoolRoot(t)
	rc := newRun(t, store, root, signedRequest(t, goodRequest()))
	rc.opts.start = reexec{env: []string{helperVerbEnv + "=fds"}}.start

	err := rc.run(t)
	if err != nil {
		t.Fatalf("Run = %v:\n%s", err, rc.stdout)
	}

	var open []fileID

	for _, r := range records(t, rc.stdout.Bytes()) {
		if r.Kind == progress.KindLog {
			must(t, json.Unmarshal([]byte(bodyOf[progress.LogBody](t, r).Line), &open))
		}
	}

	for name, path := range map[string]string{
		"out":              filepath.Join(root, "job-0001", "out"),
		"the lock file":    filepath.Join(ownerDir, ownerrecord.LockFile),
		"the owner record": filepath.Join(ownerDir, ownerrecord.RecordFile),
	} {
		// A file that is not there would pass the check below without
		// showing anything, so each must be there.
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		if slices.Contains(open, statID(t, path)) {
			t.Errorf("the verb process has %s open", name)
		}
	}
}

// The probe the fds helper verb runs sees a regular file that is open.
func TestTheDescriptorProbeSeesAnOpenFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "open")
	must(t, os.WriteFile(path, nil, 0o600))

	f, err := os.Open(path) //nolint:gosec // a file under the test's own directory
	must(t, err)

	defer func() { _ = f.Close() }()

	j, buf := newTestJob(t)

	_, err = runFDsVerb(t.Context(), j, nil)
	must(t, err)

	recs := records(t, append(headerLine(t), buf.Bytes()...))

	var open []fileID

	must(t, json.Unmarshal([]byte(bodyOf[progress.LogBody](t, recs[1]).Line), &open))

	if !slices.Contains(open, statID(t, path)) {
		t.Fatalf("the probe missed an open file: %v", open)
	}
}
