package cli

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/commands"
	"github.com/fivetwenty-io/ocfp-cli/internal/jobexec"
	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord/ownerrecordtest"
	"github.com/fivetwenty-io/ocfp-cli/internal/progress"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// The job commands and version run once per job on a bastion, so they keep
// no command lock and write no log file.
func TestRootExemptsJobAndOwnerRecord(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"job", "owner-record", "version"} {
		if !commandTrackingDisabled(name) {
			t.Errorf("%s takes the active-command lock", name)
		}

		if !fileLogDisabled(name) {
			t.Errorf("%s writes a log file", name)
		}
	}

	for _, name := range []string{"bootstrap", "bastion", "logs"} {
		if fileLogDisabled(name) {
			t.Errorf("%s writes no log file", name)
		}
	}
}

// runRootWithStdin wires the root command as Execute does and runs it with
// stdin and args.
func runRootWithStdin(t *testing.T, stdin []byte, args ...string) (string, string, error) {
	t.Helper()

	viper.Reset()
	t.Cleanup(viper.Reset)

	flags := setupFlags()
	root := createRootCommand()
	defineFlags(root, flags)
	bindFlagsToViper(root)

	lock := &lockInfo{}
	root.PersistentPreRun = createPreRunHandler(flags.blocName, lock)
	root.PersistentPostRun = createPostRunHandler(lock)
	RegisterCommands(root)

	var stdout, stderr bytes.Buffer

	root.SetIn(bytes.NewReader(stdin))
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	root.SetContext(t.Context())

	err := root.Execute()
	releaseCommandLock(lock)

	return stdout.String(), stderr.String(), err
}

func exemptOwnerRecord() ownerrecord.Record {
	return ownerrecord.Record{
		InstallationID:  "inst-cli-01",
		Org:             "org-cli",
		Bloc:            "bloc-cli",
		OwnerInstanceID: "instance-cli-a",
		OwnerPublicKey:  ownerrecordtest.TestKey().Public().(ed25519.PublicKey),
		Epoch:           1,
		Mode:            ownerrecord.ModeLive,
		Generation:      1,
		HeadHash:        "sha256:" + strings.Repeat("ef", 32),
		SignerID:        "instance-cli-a",
		SignedAt:        time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC),
	}
}

func exemptSign(msg []byte) ([]byte, error) {
	return ed25519.Sign(ownerrecordtest.TestKey(), msg), nil
}

// TestJobCommandsWriteNothingOutsideScratch runs the job commands through
// the root, so the root's logger and command lock run as they would on a
// bastion, and checks that nothing lands in the home directories.
func TestJobCommandsWriteNothingOutsideScratch(t *testing.T) {
	home := t.TempDir()
	ocfpHome := t.TempDir()
	scratch := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("OCFP_HOME", ocfpHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("OCFP_BLOC", "")
	t.Chdir(scratch)

	ownerDir := filepath.Join(scratch, "owner")
	spool := filepath.Join(scratch, "spool")
	r := exemptOwnerRecord()

	record, err := ownerrecord.Seal(r, exemptSign)
	require.NoError(t, err)

	recordJSON, err := json.Marshal(record)
	require.NoError(t, err)

	_, stderr, err := runRootWithStdin(t, recordJSON, "owner-record", "write", "--dir", ownerDir)
	require.NoError(t, err, "owner-record write: %s", stderr)

	payload, err := json.Marshal(jobexec.Request{
		WireContract:   progress.WireContract,
		JobID:          "job-scratch",
		Verb:           "noop",
		InstallationID: r.InstallationID,
		InstanceID:     r.OwnerInstanceID,
		Epoch:          r.Epoch,
		IssuedAt:       time.Now().UTC(),
	})
	require.NoError(t, err)

	request, err := json.Marshal(jobexec.SignedRequest{
		Format:    jobexec.RequestFormatV1,
		Payload:   payload,
		Signature: ed25519.Sign(ownerrecordtest.TestKey(), jobexec.RequestSigningBytes(payload)),
	})
	require.NoError(t, err)

	_, stderr, err = runRootWithStdin(t, request, "job", "exec", "--spool-root", spool, "--owner-dir", ownerDir)
	require.NoError(t, err, "job exec: %s", stderr)

	_, err = os.Stat(filepath.Join(spool, "job-scratch", "exit.json"))
	require.NoError(t, err, "job exec did not run")

	out, stderr, err := runRootWithStdin(t, nil, "version", "--output", "json")
	require.NoError(t, err, "version: %s", stderr)
	require.True(t, json.Valid([]byte(out)), "version printed %q", out)

	for _, dir := range []string{home, ocfpHome} {
		entries, err := os.ReadDir(dir)
		require.NoError(t, err)

		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}

		require.Empty(t, names, "the commands wrote into %s", dir)
	}
}

// A command that has reported its own failure on stderr sets the exit code,
// and the root does not print the failure a second time.
func TestReportError(t *testing.T) {
	t.Parallel()

	plain := errors.New("plain failure")
	reported := &commands.ExitError{Code: 2, Err: errors.New("OWNER_MISMATCH: already said")}

	cases := []struct {
		name  string
		err   error
		code  int
		print string
	}{
		{"plain", plain, 1, "plain failure\n"},
		{"reported", reported, 2, ""},
		{"reported and wrapped", fmt.Errorf("run: %w", reported), 2, ""},
	}

	for _, tc := range cases {
		var w bytes.Buffer

		if code := reportError(&w, tc.err); code != tc.code {
			t.Errorf("%s: exit code %d, want %d", tc.name, code, tc.code)
		}

		if w.String() != tc.print {
			t.Errorf("%s: printed %q, want %q", tc.name, w.String(), tc.print)
		}
	}
}
