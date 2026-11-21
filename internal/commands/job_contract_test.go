package commands

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/jobexec"
	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord/ownerrecordtest"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
)

// contractRequestWith is a wire contract query for jobID from the test
// owner, signed with key and issued now, with change applied before it is
// signed. A nil change leaves the query as it is.
func contractRequestWith(t *testing.T, jobID string, key ed25519.PrivateKey, change func(*jobexec.Request)) []byte {
	t.Helper()

	return jobRequestWith(t, jobID, key, func(r *jobexec.Request) {
		r.Action = jobexec.ActionWireContract
		r.Verb = ""

		if change != nil {
			change(r)
		}
	})
}

// legacyOwnerDir makes an owner directory that holds testOwnerRecord only
// under the record's old file name.
func legacyOwnerDir(t *testing.T) string {
	t.Helper()

	dir := ownerDirWith(t)

	err := os.Rename(filepath.Join(dir, ownerrecord.RecordFile), filepath.Join(dir, ownerrecord.LegacyRecordFile))
	if err != nil {
		t.Fatal(err)
	}

	return dir
}

// wantNoKeyMaterial checks that out holds neither the request's signature
// nor a key, in any encoding a message might use.
func wantNoKeyMaterial(t *testing.T, out string, request []byte) {
	t.Helper()

	var sr jobexec.SignedRequest
	if err := json.Unmarshal(request, &sr); err != nil {
		t.Fatal(err)
	}

	for _, s := range [][]byte{
		sr.Signature,
		ownerrecordtest.TestKey().Seed(),
		ownerrecordtest.TestKey().Public().(ed25519.PublicKey),
		otherKey.Seed(),
		otherKey.Public().(ed25519.PublicKey),
	} {
		if len(s) == 0 {
			continue
		}

		for _, enc := range []string{
			string(s),
			hex.EncodeToString(s),
			base64.StdEncoding.EncodeToString(s),
			base64.RawURLEncoding.EncodeToString(s),
		} {
			if strings.Contains(out, enc) {
				t.Fatalf("output %q holds key or signature material", out)
			}
		}
	}
}

// wantOwnerUnlocked proves that no shared lock is left on the owner record in
// dir by taking the exclusive lock, through a Replace that expects a
// generation the record never has and so writes nothing. A directory that
// does not exist holds no lock and is left as it is.
func wantOwnerUnlocked(t *testing.T, dir string) {
	t.Helper()

	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return
	}

	sf, err := ownerrecord.Seal(testOwnerRecord(), testSign)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	never := uint64(1 << 40)

	err = ownerrecord.NewDirStore(dir).Replace(ctx, &never, sf)
	if !errors.Is(err, ownerrecord.ErrRecordChanged) && !errors.Is(err, ownerrecord.ErrStoredRecordInvalid) {
		t.Fatalf("the exclusive lock was not granted after job contract: %v", err)
	}
}

func TestJobContractOwnerDirDefault(t *testing.T) {
	t.Parallel()

	ownerDir := ownerDirWith(t)

	var opened []string

	cmd := newJobCmd(func(dir string) *ownerrecord.DirStore {
		opened = append(opened, dir)

		return ownerrecord.NewDirStore(dir)
	}, ownerrecord.DefaultDir)

	contractCmd, _, err := cmd.Find([]string{"contract"})
	if err != nil {
		t.Fatal(err)
	}

	if def := contractCmd.Flags().Lookup("owner-dir").DefValue; def != ownerrecord.DefaultDir {
		t.Fatalf("--owner-dir defaults to %q, want %q", def, ownerrecord.DefaultDir)
	}

	if contractCmd.Flags().Lookup("spool-root") != nil {
		t.Fatal("job contract takes --spool-root")
	}

	res := runCmd(t, cmd, bytes.NewReader(contractRequestWith(t, "job-owner-dir", ownerrecordtest.TestKey(), nil)),
		"contract", "--owner-dir", ownerDir)
	if res.code != 0 {
		t.Fatalf("job contract = %d, %v, stderr %q", res.code, res.err, res.stderr)
	}

	if !slices.Equal(opened, []string{ownerDir}) {
		t.Fatalf("the command opened the owner stores %q, want only %q", opened, ownerDir)
	}
}

func TestJobContractExitCodes(t *testing.T) {
	t.Parallel()

	for _, contract := range []int{progress.WireContract, progress.WireContract + 1} {
		ownerDir := ownerDirWith(t)
		request := contractRequestWith(t, "job-ok", ownerrecordtest.TestKey(), func(r *jobexec.Request) {
			r.WireContract = contract
		})

		res := runCmd(t, NewJobCmd(), bytes.NewReader(request), "contract", "--owner-dir", ownerDir)
		if res.code != 0 || res.err != nil {
			t.Fatalf("contract %d: job contract = %d, %v, stderr %q", contract, res.code, res.err, res.stderr)
		}

		if want := `{"format":1,"wireContract":3,"jobId":"job-ok"}` + "\n"; res.stdout != want {
			t.Fatalf("contract %d: stdout = %q, want %q", contract, res.stdout, want)
		}

		if res.stderr != "" {
			t.Fatalf("contract %d: stderr = %q, want nothing", contract, res.stderr)
		}

		wantOwnerUnlocked(t, ownerDir)

		entries, err := os.ReadDir(ownerDir)
		if err != nil {
			t.Fatal(err)
		}

		for _, e := range entries {
			if e.Name() != ownerrecord.RecordFile && e.Name() != ownerrecord.LockFile {
				t.Fatalf("job contract left %q in the owner directory", e.Name())
			}
		}
	}

	refusals := []struct {
		name     string
		ownerDir func(t *testing.T) string
		key      ed25519.PrivateKey
		code     progress.Code
		echo     bool
		change   func(*jobexec.Request)
	}{
		{"unsigned", ownerDirWith, otherKey, progress.CodeRequestUnsigned, false, nil},
		{"no owner record", func(t *testing.T) string { return filepath.Join(t.TempDir(), "owner") }, ownerrecordtest.TestKey(), progress.CodeOwnerRecordMissing, false, nil},
		{"legacy owner record only", legacyOwnerDir, ownerrecordtest.TestKey(), progress.CodeOwnerRecordMissing, false, nil},
		{"owner record does not open", badOwnerDir, ownerrecordtest.TestKey(), progress.CodeOwnerRecordInvalid, false, nil},
		{"another installation", ownerDirWith, ownerrecordtest.TestKey(), progress.CodeOwnerMismatch, true, func(r *jobexec.Request) {
			r.InstallationID = "inst-cmd-02"
		}},
		{"another org", ownerDirWith, ownerrecordtest.TestKey(), progress.CodeOrgBlocMismatch, true, func(r *jobexec.Request) { r.Org = "org-other" }},
		{"another bloc", ownerDirWith, ownerrecordtest.TestKey(), progress.CodeOrgBlocMismatch, true, func(r *jobexec.Request) { r.Bloc = "bloc-other" }},
		{"older wire contract", ownerDirWith, ownerrecordtest.TestKey(), progress.CodeWireContractUnsupported, true, func(r *jobexec.Request) {
			r.WireContract = progress.WireContract - 1
		}},
		{"run", ownerDirWith, ownerrecordtest.TestKey(), progress.CodeJobRequestInvalid, true, func(r *jobexec.Request) {
			r.Action, r.Verb = jobexec.ActionRun, "noop"
		}},
		{"cancel", ownerDirWith, ownerrecordtest.TestKey(), progress.CodeJobRequestInvalid, true, func(r *jobexec.Request) {
			r.Action = jobexec.ActionCancel
		}},
		{"verb", ownerDirWith, ownerrecordtest.TestKey(), progress.CodeJobRequestInvalid, true, func(r *jobexec.Request) { r.Verb = "noop" }},
		{"env", ownerDirWith, ownerrecordtest.TestKey(), progress.CodeJobRequestInvalid, true, func(r *jobexec.Request) {
			r.Env = map[string]string{"LANG": "C"}
		}},
		{"interactive", ownerDirWith, ownerrecordtest.TestKey(), progress.CodeJobRequestInvalid, true, func(r *jobexec.Request) {
			r.Interactive = true
		}},
	}

	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := contractRequestWith(t, "job-refused", tc.key, tc.change)

			ownerDir := tc.ownerDir(t)

			res := runCmd(t, NewJobCmd(), bytes.NewReader(request), "contract", "--owner-dir", ownerDir)
			if res.code != 2 {
				t.Fatalf("job contract = %d, %v, want exit code 2", res.code, res.err)
			}

			if strings.Count(res.stdout, "\n") != 1 || !strings.HasSuffix(res.stdout, "\n") {
				t.Fatalf("stdout = %q, want exactly one line", res.stdout)
			}

			var line jobexec.WireContractRefusal

			dec := json.NewDecoder(strings.NewReader(res.stdout))
			dec.DisallowUnknownFields()

			if err := dec.Decode(&line); err != nil {
				t.Fatalf("stdout %q does not decode as a refusal: %v", res.stdout, err)
			}

			if line.Format != 1 || line.Error.Code != tc.code || line.Error.Message == "" {
				t.Fatalf("stdout = %q, want format 1 and a %s refusal", res.stdout, tc.code)
			}

			wantJobID := ""
			if tc.echo {
				wantJobID = "job-refused"
			}

			if line.JobID != wantJobID {
				t.Fatalf("stdout names job %q, want %q", line.JobID, wantJobID)
			}

			if !tc.echo && strings.Contains(res.stdout+res.stderr, "job-refused") {
				t.Fatalf("output echoes the job id of a request that did not verify: %q %q", res.stdout, res.stderr)
			}

			wantStderr := "job contract: " + string(tc.code) + ": " + line.Error.Message + "\n"
			if res.stderr != wantStderr {
				t.Fatalf("stderr = %q, want %q", res.stderr, wantStderr)
			}

			wantNoKeyMaterial(t, res.stdout+res.stderr, request)
			wantOwnerUnlocked(t, ownerDir)
		})
	}

	failures := []struct {
		name  string
		stdin io.Reader
		args  func(t *testing.T) []string
	}{
		{"unreadable stdin", errReader{}, func(t *testing.T) []string {
			return []string{"contract", "--owner-dir", ownerDirWith(t)}
		}},
		{"empty stdin", strings.NewReader(""), func(t *testing.T) []string {
			return []string{"contract", "--owner-dir", ownerDirWith(t)}
		}},
		{"relative owner dir", nil, func(*testing.T) []string {
			return []string{"contract", "--owner-dir", "owner"}
		}},
		{"spool root", nil, func(t *testing.T) []string {
			return []string{"contract", "--owner-dir", ownerDirWith(t), "--spool-root", filepath.Join(t.TempDir(), "spool")}
		}},
		{"an argument", nil, func(t *testing.T) []string {
			return []string{"contract", "--owner-dir", ownerDirWith(t), "extra"}
		}},
	}

	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stdin := tc.stdin
			if stdin == nil {
				stdin = bytes.NewReader(contractRequestWith(t, "job-failed", ownerrecordtest.TestKey(), nil))
			}

			res := runCmd(t, NewJobCmd(), stdin, tc.args(t)...)
			if res.code != 1 {
				t.Fatalf("job contract = %d, %v, want exit code 1", res.code, res.err)
			}

			if res.stdout != "" {
				t.Fatalf("stdout = %q, want nothing", res.stdout)
			}
		})
	}
}

// job exec refuses a wire contract query before it makes a spool.
func TestJobExecRefusesAWireContractQuery(t *testing.T) {
	t.Parallel()

	spool := filepath.Join(t.TempDir(), "spool")

	res := runCmd(t, NewJobCmd(), bytes.NewReader(contractRequestWith(t, "job-query", ownerrecordtest.TestKey(), nil)),
		"exec", "--spool-root", spool, "--owner-dir", ownerDirWith(t))
	if res.code != 2 {
		t.Fatalf("job exec = %d, %v, want exit code 2", res.code, res.err)
	}

	want := []string{"header", "error:" + string(progress.CodeJobRequestInvalid), "result"}
	if got := streamKinds(t, res.stdout); !slices.Equal(got, want) {
		t.Fatalf("stream kinds = %v, want %v", got, want)
	}

	if entries, err := os.ReadDir(spool); err == nil && len(entries) != 0 {
		t.Fatalf("a refused query left %d entries under the spool root", len(entries))
	}
}
