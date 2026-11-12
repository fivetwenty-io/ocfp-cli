package commands

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/jobexec"
	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord/ownerrecordtest"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/spf13/cobra"
)

// otherKey signs requests and records that the owner record does not name.
var otherKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x3c}, ed25519.SeedSize))

// testOwnerRecord is the owner record the command tests write. It names the
// published test key, which signs nothing real.
func testOwnerRecord() ownerrecord.Record {
	return ownerrecord.Record{
		InstallationID:  "inst-cmd-01",
		OwnerInstanceID: "instance-cmd-a",
		OwnerPublicKey:  ownerrecordtest.TestKey().Public().(ed25519.PublicKey),
		Epoch:           1,
		Mode:            ownerrecord.ModeLive,
		Generation:      1,
		HeadPosition:    0,
		HeadHash:        "sha256:" + strings.Repeat("cd", 32),
		SignerID:        "instance-cmd-a",
		SignedAt:        time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC),
	}
}

func testSign(msg []byte) ([]byte, error) {
	return ed25519.Sign(ownerrecordtest.TestKey(), msg), nil
}

// sealedOwner seals r with the test key and returns its form on stdin.
func sealedOwner(t *testing.T, r ownerrecord.Record) []byte {
	t.Helper()

	sf, err := ownerrecord.Seal(r, testSign)
	if err != nil {
		t.Fatal(err)
	}

	return jsonLine(t, sf)
}

func jsonLine(t *testing.T, v any) []byte {
	t.Helper()

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}

	return append(b, '\n')
}

// ownerDirWith makes an owner directory that holds testOwnerRecord.
func ownerDirWith(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "owner")

	sf, err := ownerrecord.Seal(testOwnerRecord(), testSign)
	if err != nil {
		t.Fatal(err)
	}

	if err := ownerrecord.NewDirStore(dir).Replace(t.Context(), nil, sf); err != nil {
		t.Fatal(err)
	}

	return dir
}

// jobRequest is a no-op request for jobID from the test owner, signed with
// key and issued now.
func jobRequest(t *testing.T, jobID string, key ed25519.PrivateKey) []byte {
	t.Helper()

	r := testOwnerRecord()

	payload, err := json.Marshal(jobexec.Request{
		WireContract:   progress.WireContract,
		JobID:          jobID,
		Verb:           "noop",
		InstallationID: r.InstallationID,
		InstanceID:     r.OwnerInstanceID,
		Epoch:          r.Epoch,
		IssuedAt:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	return jsonLine(t, jobexec.SignedRequest{
		Format:    jobexec.RequestFormatV1,
		Payload:   payload,
		Signature: ed25519.Sign(key, jobexec.RequestSigningBytes(payload)),
	})
}

// cmdResult is what one run of a command left behind.
type cmdResult struct {
	stdout string
	stderr string
	err    error
	code   int
}

func runCmd(t *testing.T, cmd *cobra.Command, stdin io.Reader, args ...string) cmdResult {
	t.Helper()

	var stdout, stderr bytes.Buffer

	cmd.SetIn(stdin)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	cmd.SetContext(t.Context())

	err := cmd.Execute()

	return cmdResult{stdout: stdout.String(), stderr: stderr.String(), err: err, code: ExitCode(err)}
}

// streamKinds decodes a progress stream and returns the kind of each record,
// with the code of each error record.
func streamKinds(t *testing.T, stream string) []string {
	t.Helper()

	var kinds []string

	sc := bufio.NewScanner(strings.NewReader(stream))
	for sc.Scan() {
		var r progress.Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("stream line %q does not decode: %v", sc.Text(), err)
		}

		kind := string(r.Kind)

		if r.Kind == progress.KindError {
			var body progress.ErrorBody
			if err := json.Unmarshal(r.Body, &body); err != nil {
				t.Fatal(err)
			}

			kind += ":" + string(body.Code)
		}

		kinds = append(kinds, kind)
	}

	return kinds
}

func TestOwnerDirFlagOverridesDefault(t *testing.T) {
	t.Parallel()

	ownerDir := ownerDirWith(t)
	spool := filepath.Join(t.TempDir(), "spool")

	var opened []string

	cmd := newJobCmd(func(dir string) *ownerrecord.DirStore {
		opened = append(opened, dir)

		return ownerrecord.NewDirStore(dir)
	})

	execCmd, _, err := cmd.Find([]string{"exec"})
	if err != nil {
		t.Fatal(err)
	}

	if def := execCmd.Flags().Lookup("owner-dir").DefValue; def != ownerrecord.DefaultDir {
		t.Fatalf("--owner-dir defaults to %q, want %q", def, ownerrecord.DefaultDir)
	}

	res := runCmd(t, cmd, bytes.NewReader(jobRequest(t, "job-owner-dir", ownerrecordtest.TestKey())),
		"exec", "--spool-root", spool, "--owner-dir", ownerDir)
	if res.code != 0 {
		t.Fatalf("job exec = %d, %v, stderr %q", res.code, res.err, res.stderr)
	}

	if !slices.Equal(opened, []string{ownerDir}) {
		t.Fatalf("the command opened the owner stores %q, want only %q", opened, ownerDir)
	}
}

func TestJobExecExitCodes(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		spool := filepath.Join(t.TempDir(), "spool")

		res := runCmd(t, NewJobCmd(), bytes.NewReader(jobRequest(t, "job-ok", ownerrecordtest.TestKey())),
			"exec", "--spool-root", spool, "--owner-dir", ownerDirWith(t))
		if res.code != 0 || res.err != nil {
			t.Fatalf("job exec = %d, %v, stderr %q", res.code, res.err, res.stderr)
		}

		want := []string{"header", "event", "log", "event", "result"}
		if got := streamKinds(t, res.stdout); !slices.Equal(got, want) {
			t.Fatalf("stream kinds = %v, want %v", got, want)
		}

		if res.stderr != "" {
			t.Fatalf("stderr = %q, want nothing", res.stderr)
		}

		if _, err := os.Stat(filepath.Join(spool, "job-ok", "exit.json")); err != nil {
			t.Fatalf("the spool has no exit.json: %v", err)
		}
	})

	refusals := []struct {
		name     string
		ownerDir func(t *testing.T) string
		key      ed25519.PrivateKey
		code     progress.Code
	}{
		{"unsigned", ownerDirWith, otherKey, progress.CodeRequestUnsigned},
		{"no owner record", func(t *testing.T) string { return filepath.Join(t.TempDir(), "owner") }, ownerrecordtest.TestKey(), progress.CodeOwnerRecordMissing},
		{"owner record does not open", badOwnerDir, ownerrecordtest.TestKey(), progress.CodeOwnerRecordInvalid},
	}

	for _, tc := range refusals {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			spool := filepath.Join(t.TempDir(), "spool")

			res := runCmd(t, NewJobCmd(), bytes.NewReader(jobRequest(t, "job-refused", tc.key)),
				"exec", "--spool-root", spool, "--owner-dir", tc.ownerDir(t))
			if res.code != 2 {
				t.Fatalf("job exec = %d, %v, want exit code 2", res.code, res.err)
			}

			want := []string{"header", "error:" + string(tc.code), "result"}
			if got := streamKinds(t, res.stdout); !slices.Equal(got, want) {
				t.Fatalf("stream kinds = %v, want %v", got, want)
			}

			if !strings.Contains(res.stderr, string(tc.code)) {
				t.Fatalf("stderr %q does not name %s", res.stderr, tc.code)
			}

			if _, err := os.Stat(filepath.Join(spool, "job-refused")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a refused job has a spool: %v", err)
			}
		})
	}

	failures := []struct {
		name  string
		stdin io.Reader
		args  func(t *testing.T) []string
	}{
		{"unreadable stdin", errReader{}, func(t *testing.T) []string {
			return []string{"exec", "--spool-root", filepath.Join(t.TempDir(), "spool"), "--owner-dir", ownerDirWith(t)}
		}},
		{"empty stdin", strings.NewReader(""), func(t *testing.T) []string {
			return []string{"exec", "--spool-root", filepath.Join(t.TempDir(), "spool"), "--owner-dir", ownerDirWith(t)}
		}},
		{"relative spool root", nil, func(t *testing.T) []string {
			return []string{"exec", "--spool-root", "spool", "--owner-dir", ownerDirWith(t)}
		}},
		{"relative owner dir", nil, func(t *testing.T) []string {
			return []string{"exec", "--spool-root", filepath.Join(t.TempDir(), "spool"), "--owner-dir", "owner"}
		}},
		{"no spool root", nil, func(t *testing.T) []string {
			return []string{"exec", "--owner-dir", ownerDirWith(t)}
		}},
	}

	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stdin := tc.stdin
			if stdin == nil {
				stdin = bytes.NewReader(jobRequest(t, "job-failed", ownerrecordtest.TestKey()))
			}

			res := runCmd(t, NewJobCmd(), stdin, tc.args(t)...)
			if res.code != 1 {
				t.Fatalf("job exec = %d, %v, want exit code 1", res.code, res.err)
			}

			if res.stdout != "" {
				t.Fatalf("stdout = %q, want nothing", res.stdout)
			}
		})
	}
}

// badOwnerDir makes an owner directory whose record's signature is broken.
func badOwnerDir(t *testing.T) string {
	t.Helper()

	dir := ownerDirWith(t)
	path := filepath.Join(dir, ownerrecord.RecordFile)

	var sf ownerrecord.Signed

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := json.Unmarshal(data, &sf); err != nil {
		t.Fatal(err)
	}

	sf.Signature[0] ^= 0x01

	if err := os.WriteFile(path, jsonLine(t, sf), 0o600); err != nil {
		t.Fatal(err)
	}

	return dir
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("stdin is gone") }

func TestExitCode(t *testing.T) {
	t.Parallel()

	plain := errors.New("plain")
	coded := &ExitError{Code: 2, Err: plain}

	cases := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{plain, 1},
		{coded, 2},
		{errors.Join(errors.New("context"), coded), 2},
		{&ExitError{Code: 1, Err: plain}, 1},
		// A failure never exits 0 or with a negative code.
		{&ExitError{Code: 0, Err: plain}, 1},
		{&ExitError{Code: -2, Err: plain}, 1},
	}

	for _, tc := range cases {
		if got := ExitCode(tc.err); got != tc.want {
			t.Errorf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}

	if !errors.Is(coded, plain) {
		t.Error("an ExitError hides the error it carries")
	}

	if msg := (&ExitError{Code: 2}).Error(); msg == "" {
		t.Error("an ExitError with no error has no message")
	}
}

// The environment that turns this test binary into a run of job exec, for
// the tests that need a process of its own.
const (
	jobExecHelperEnv   = "OCFP_TEST_JOB_EXEC_HELPER"
	jobExecSpoolEnv    = "OCFP_TEST_JOB_EXEC_SPOOL"
	jobExecOwnerDirEnv = "OCFP_TEST_JOB_EXEC_OWNER_DIR"
)

// runJobExecHelper runs job exec with the real stdin and stdout, as the
// binary would, and returns its exit code. TestMain calls it in place of the
// tests when jobExecHelperEnv is set.
func runJobExecHelper() int {
	cmd := NewJobCmd()
	cmd.SetArgs([]string{
		"exec",
		"--spool-root", os.Getenv(jobExecSpoolEnv),
		"--owner-dir", os.Getenv(jobExecOwnerDirEnv),
	})

	return ExitCode(cmd.Execute())
}

// A requester that goes away closes the read end of the job's stdout. The
// job must not die of SIGPIPE on its next write. It finishes into the spool,
// and a later attach replays the whole stream.
func TestJobExecSurvivesAVanishedRequester(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("the job spool runs only on Unix")
	}

	spool := filepath.Join(t.TempDir(), "spool")
	ownerDir := ownerDirWith(t)
	request := jobRequest(t, "job-vanished", ownerrecordtest.TestKey())

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	// The requester has gone before the job writes anything.
	if err := pr.Close(); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer

	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^$") //nolint:gosec // this test binary
	child.Env = append(os.Environ(),
		jobExecHelperEnv+"=1",
		jobExecSpoolEnv+"="+spool,
		jobExecOwnerDirEnv+"="+ownerDir,
	)
	child.Stdin = bytes.NewReader(request)
	child.Stdout = pw
	child.Stderr = &stderr

	runErr := child.Run()

	if err := pw.Close(); err != nil {
		t.Fatal(err)
	}

	if code := child.ProcessState.ExitCode(); code == -1 {
		t.Fatalf("job exec was killed (%s) when its requester went away, stderr %q", child.ProcessState, stderr.String())
	}

	if code := child.ProcessState.ExitCode(); code != 1 {
		t.Fatalf("job exec = %d (%v), want 1 for the failed stdout, stderr %q", code, runErr, stderr.String())
	}

	dir := filepath.Join(spool, "job-vanished")

	if _, err := os.Stat(filepath.Join(dir, "exit.json")); err != nil {
		t.Fatalf("the job did not finish into its spool: %v", err)
	}

	out, err := os.ReadFile(filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}

	res := runCmd(t, NewJobCmd(), bytes.NewReader(request), "exec", "--spool-root", spool, "--owner-dir", ownerDir)
	if res.code != 0 {
		t.Fatalf("the attach = %d, %v, stderr %q", res.code, res.err, res.stderr)
	}

	if res.stdout != string(out) {
		t.Fatalf("the attach streamed\n%s\nwant the spool\n%s", res.stdout, out)
	}

	want := []string{"header", "event", "log", "event", "result"}
	if got := streamKinds(t, res.stdout); !slices.Equal(got, want) {
		t.Fatalf("stream kinds = %v, want %v", got, want)
	}
}

// A dropped terminal session sends SIGHUP as well as closing the pipes, and
// the job must survive both to finish into its spool.
func TestJobExecIgnoresHangUpAndBrokenPipe(t *testing.T) {
	t.Parallel()

	res := runCmd(t, NewJobCmd(), strings.NewReader(""), "exec", "--spool-root", "relative", "--owner-dir", ownerDirWith(t))
	if res.code != 1 {
		t.Fatalf("job exec = %d, %v, want 1", res.code, res.err)
	}

	for _, sig := range []os.Signal{syscall.SIGPIPE, syscall.SIGHUP} {
		if !signal.Ignored(sig) {
			t.Errorf("job exec does not ignore %s", sig)
		}
	}
}
