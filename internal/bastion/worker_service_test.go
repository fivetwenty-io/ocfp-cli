package bastion

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/fivetwenty-io/ocfp-cli/internal/bastion/ssh"
	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/fivetwenty-io/ocfp-cli/internal/workerunit"
)

const (
	workerBloc = "ocfp-lab-drgao"

	// workerSeed is shaped like the join key's seed. No command, log line,
	// or error may ever repeat it.
	workerSeed = "SAAGNWHW5QPDXQ2YAN3ZTJQ6IPSQ4FCRJC5CA5MGHFQOIQP7D6CEVJ37NQ"

	workerHostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl root@bastion"
)

var workerBinary = []byte("\x7fELF pretend worker binary")

func workerSum(b []byte) string {
	s := sha256.Sum256(b)

	return hex.EncodeToString(s[:])
}

// workerJoinDoc is a join file whose amd64 sum names workerBinary.
func workerJoinDoc() map[string]any {
	return map[string]any{
		"url":          "wss://manager.example.com:443",
		"installation": "inst-1",
		"org":          "acme",
		"bloc":         workerBloc,
		"joinAccount":  "ADKD4E6LQ5Z4T3SC3UTXHBLX4A2M6ZCG5JAK7P4W2ZTR2NTUFJUVXUUW",
		"joinKey":      workerSeed,
		"binary": map[string]any{
			"version": "1.2.3",
			"sha256": map[string]string{
				"linux/amd64": workerSum(workerBinary),
				"linux/arm64": strings.Repeat("ab", 32),
			},
		},
		"issuedAt": "2026-11-24T20:00:00Z",
	}
}

// writeWorkerBundle writes a bundle of join and binary and returns its
// path and the join file's bytes.
func writeWorkerBundle(t *testing.T, join map[string]any, binary []byte) (string, []byte) {
	t.Helper()

	joinBody, err := json.Marshal(join)
	require.NoError(t, err)

	var buf bytes.Buffer

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	for name, body := range map[string][]byte{
		workerunit.BundleJoinFile: joinBody,
		workerunit.BundleBinary:   binary,
	} {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}))

		_, err := tw.Write(body)
		require.NoError(t, err)
	}

	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())

	path := filepath.Join(t.TempDir(), "bloc-worker-bundle.tar.gz")
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))

	return path, joinBody
}

type workerTransfer struct {
	remote string
	body   []byte
}

// workerMockSSHClient answers the worker phase's commands by name. The
// probe reports what the test sets, the join file and binary scripts
// report a change as the real ones do, and every other script reports a
// change only when the test asks it to.
type workerMockSSHClient struct {
	t         *testing.T
	probe     string
	changed   map[string]bool
	fail      string
	names     map[string]string
	commands  []string
	steps     []string
	transfers []workerTransfer
}

func newWorkerMock(t *testing.T, probe string) *workerMockSSHClient {
	t.Helper()

	mock := &workerMockSSHClient{t: t, probe: probe, changed: map[string]bool{}, names: map[string]string{}}

	parsed, err := workerunit.ParseProbe(probe)
	if err != nil {
		parsed = workerunit.Probe{User: "ubuntu", Group: "ubuntu", Home: "/home/ubuntu"}
	}

	rec, err := workerunit.NewRecord(workerBloc, parsed.User)
	require.NoError(t, err)

	unit, err := workerunit.Render(workerBloc, workerunit.UnitParams{User: parsed.User, Home: parsed.Home})
	require.NoError(t, err)

	add := func(name string, cmd string, err error) {
		require.NoError(t, err, name)

		mock.names[cmd] = name
	}

	cmd, err := workerunit.ProbeCommand(workerBloc)
	add("probe", cmd, err)
	cmd, err = workerunit.DirectoryCommand(workerBloc, parsed)
	add("directory", cmd, err)
	cmd, err = workerunit.LoopbackCommand(workerBloc)
	add("loopback", cmd, err)
	cmd, err = workerunit.PrepareCommand(workerBloc)
	add("prepare", cmd, err)
	cmd, err = workerunit.RecordCommand(workerBloc, rec)
	add("record", cmd, err)
	cmd, err = workerunit.InstallCommand(workerBloc, unit)
	add("unit", cmd, err)
	cmd, err = workerunit.StartCommand(workerBloc, false)
	add("start", cmd, err)
	cmd, err = workerunit.StartCommand(workerBloc, true)
	add("restart", cmd, err)

	return mock
}

func (c *workerMockSSHClient) Connect(_ context.Context) error { return nil }

func (c *workerMockSSHClient) TransferFile(_ context.Context, local, remote string, _ ssh.TransferOptions) error {
	body, err := os.ReadFile(local)
	require.NoError(c.t, err)

	c.transfers = append(c.transfers, workerTransfer{remote: remote, body: body})
	c.steps = append(c.steps, "transfer "+filepath.Base(remote))

	return nil
}

func (c *workerMockSSHClient) ExecuteCommand(_ context.Context, cmd string) (*ssh.CommandResult, error) {
	c.commands = append(c.commands, cmd)

	name, ok := c.names[cmd]

	switch {
	case ok:
	case strings.Contains(cmd, ".join.json.upload"):
		name = "join file"
	case strings.Contains(cmd, ".bloc-worker.upload"):
		name = "binary"
	default:
		name = "unknown"
	}

	c.steps = append(c.steps, name)

	if name == c.fail {
		return &ssh.CommandResult{ExitCode: 1, Stderr: "sudo: a password is required"}, errors.New("exit status 1")
	}

	switch {
	case name == "probe":
		return &ssh.CommandResult{Stdout: c.probe}, nil
	case name == "join file" || name == "binary" || c.changed[name]:
		return &ssh.CommandResult{Stdout: workerunit.ChangeMarker + " " + name + "\n"}, nil
	default:
		return &ssh.CommandResult{}, nil
	}
}

func (c *workerMockSSHClient) CreateTunnel(_ context.Context, _, _ int) error { return nil }

func (c *workerMockSSHClient) Close() error { return nil }

// workerProbe is what a bastion prints with the given installed sums.
func workerProbe(arch, binarySum, joinSum string) string {
	return strings.Join([]string{
		"Welcome to the bastion",
		"user=ubuntu",
		"group=ubuntu",
		"home=/home/ubuntu",
		"arch=" + arch,
		"binary_sha256=" + binarySum,
		"join_sha256=" + joinSum,
		"host_key=" + workerHostKey,
		"",
	}, "\n")
}

type workerHarness struct {
	m    *Manager
	mock *workerMockSSHClient
	out  *bytes.Buffer
	logs *observer.ObservedLogs
}

func newWorkerHarness(t *testing.T, probe string, dryRun bool) *workerHarness {
	t.Helper()

	core, logs := observer.New(zapcore.DebugLevel)
	out := &bytes.Buffer{}
	mock := newWorkerMock(t, probe)

	m := newMinimalManager(newBaseConfig(workerBloc, "pve"))
	m.log = zap.New(core).Sugar()
	m.sshClient = mock
	m.options = &ProvisioningOptions{DryRun: dryRun, ProgressOut: out}

	return &workerHarness{m: m, mock: mock, out: out, logs: logs}
}

func bundleEnv(path string) func(string) string {
	return func(key string) string {
		if key == config.EnvWorkerBundle {
			return path
		}

		return ""
	}
}

// assertNoSeed checks that the join key reached no command, log line,
// operator output, or error.
func assertNoSeed(t *testing.T, h *workerHarness, err error) {
	t.Helper()

	for _, cmd := range h.mock.commands {
		assert.NotContains(t, cmd, workerSeed)
		assert.NotContains(t, cmd, workerSeed[:12])
	}

	for _, entry := range h.logs.All() {
		line := entry.Message

		for k, v := range entry.ContextMap() {
			line += " " + k + "=" + strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(jsonString(t, v), "\n", " "), "\t", " "))
		}

		assert.NotContains(t, line, workerSeed)
		assert.NotContains(t, line, workerSeed[:12])
	}

	assert.NotContains(t, h.out.String(), workerSeed)

	if err != nil {
		assert.NotContains(t, err.Error(), workerSeed)
		assert.NotContains(t, err.Error(), workerSeed[:12])
	}
}

func jsonString(t *testing.T, v any) string {
	t.Helper()

	b, err := json.Marshal(v)
	require.NoError(t, err)

	return string(b)
}

func TestPhaseLists_InstallTheWorkerAfterTheBootUnitAndBeforeTheHealthCheck(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))

	for list, names := range map[string][]string{
		"sequential": sequentialPhaseNameOrder(m),
		"parallel":   parallelModePhaseNameOrder(m),
		"local":      localPhaseNameOrder(m),
	} {
		worker := indexOf(names, "worker_service")
		require.NotEqual(t, -1, worker, "%s list is missing worker_service", list)
		assert.Less(t, indexOf(names, "vault_boot_unit"), worker, "%s list installs the worker after vault_boot_unit", list)
		assert.Less(t, worker, indexOf(names, "health_check"), "%s list installs the worker before health_check", list)
		assert.Less(t, indexOf(names, "genesis_secrets_providers"), worker,
			"%s list installs the worker once ocfp is configured", list)
	}
}

func TestInstallWorker_NoBundleIsANoOp(t *testing.T) {
	t.Parallel()

	h := newWorkerHarness(t, workerProbe("x86_64", "", ""), false)

	require.NoError(t, h.m.installWorkerFrom(context.Background(), bundleEnv("")))
	assert.Empty(t, h.mock.steps, "with no bundle the phase reaches nothing on the bastion")
	assert.Equal(t, 1, h.logs.FilterMessageSnippet("no worker bundle").Len(), "the skip is logged")
}

func TestInstallWorker_FreshBastion(t *testing.T) {
	t.Parallel()

	bundle, joinBody := writeWorkerBundle(t, workerJoinDoc(), workerBinary)
	h := newWorkerHarness(t, workerProbe("x86_64", "", ""), false)

	err := h.m.installWorkerFrom(context.Background(), bundleEnv(bundle))
	require.NoError(t, err)

	assert.Equal(t, []string{
		"probe",
		"directory",
		"transfer .join.json.upload",
		"join file",
		"transfer .bloc-worker.upload",
		"binary",
		"loopback",
		"prepare",
		"record",
		"unit",
		"restart",
	}, h.mock.steps)

	require.Len(t, h.mock.transfers, 2)
	assert.Equal(t, "/data/bloc-worker/ocfp-lab-drgao/.join.json.upload", h.mock.transfers[0].remote)
	assert.Equal(t, joinBody, h.mock.transfers[0].body, "the join file is installed byte for byte")
	assert.Equal(t, "/data/bloc-worker/ocfp-lab-drgao/.bloc-worker.upload", h.mock.transfers[1].remote)
	assert.Equal(t, workerBinary, h.mock.transfers[1].body)

	joinCmd, err := workerunit.JoinFileCommand(workerBloc, workerSum(joinBody))
	require.NoError(t, err)
	assert.Contains(t, h.mock.commands, joinCmd, "the join file is checked against its sum on the bastion")

	binaryCmd, err := workerunit.BinaryCommand(workerBloc, workerSum(workerBinary))
	require.NoError(t, err)
	assert.Contains(t, h.mock.commands, binaryCmd, "the binary is checked against its sum on the bastion")

	assertNoSeed(t, h, err)
}

func TestInstallWorker_PrintsTheHostKeyFingerprint(t *testing.T) {
	t.Parallel()

	bundle, _ := writeWorkerBundle(t, workerJoinDoc(), workerBinary)
	h := newWorkerHarness(t, workerProbe("x86_64", "", ""), false)

	require.NoError(t, h.m.installWorkerFrom(context.Background(), bundleEnv(bundle)))

	fp, err := workerunit.HostKeyFingerprint(workerHostKey)
	require.NoError(t, err)

	out := h.out.String()
	assert.Contains(t, out, fp, "the operator sees the bastion's host key fingerprint")
	assert.Contains(t, out, workerunit.FingerprintTail(fp), "and the characters to type to approve the bloc")
	assert.Equal(t, 1, h.logs.FilterField(zap.String("host_key_fingerprint", fp)).Len(), "and the log records it")
}

func TestInstallWorker_SameSumsChangeNothing(t *testing.T) {
	t.Parallel()

	bundle, joinBody := writeWorkerBundle(t, workerJoinDoc(), workerBinary)
	h := newWorkerHarness(t, workerProbe("x86_64", workerSum(workerBinary), workerSum(joinBody)), false)

	require.NoError(t, h.m.installWorkerFrom(context.Background(), bundleEnv(bundle)))

	assert.Empty(t, h.mock.transfers, "a rerun with the same sums uploads nothing")
	assert.Equal(t, []string{"probe", "directory", "loopback", "prepare", "record", "unit", "start"}, h.mock.steps,
		"and starts the worker only if it is not already running")
}

func TestInstallWorker_ADifferentBinaryReplacesAndRestarts(t *testing.T) {
	t.Parallel()

	bundle, joinBody := writeWorkerBundle(t, workerJoinDoc(), workerBinary)
	h := newWorkerHarness(t, workerProbe("x86_64", strings.Repeat("99", 32), workerSum(joinBody)), false)

	require.NoError(t, h.m.installWorkerFrom(context.Background(), bundleEnv(bundle)))

	require.Len(t, h.mock.transfers, 1)
	assert.Equal(t, workerBinary, h.mock.transfers[0].body)
	assert.Contains(t, h.mock.steps, "binary")
	assert.Equal(t, "restart", h.mock.steps[len(h.mock.steps)-1])
	assert.NotContains(t, h.mock.steps, "start")
}

func TestInstallWorker_AChangeInAnyStepRestarts(t *testing.T) {
	t.Parallel()

	for _, step := range []string{"loopback", "prepare", "record", "unit"} {
		t.Run(step, func(t *testing.T) {
			t.Parallel()

			bundle, joinBody := writeWorkerBundle(t, workerJoinDoc(), workerBinary)
			h := newWorkerHarness(t, workerProbe("x86_64", workerSum(workerBinary), workerSum(joinBody)), false)
			h.mock.changed[step] = true

			require.NoError(t, h.m.installWorkerFrom(context.Background(), bundleEnv(bundle)))
			assert.Equal(t, "restart", h.mock.steps[len(h.mock.steps)-1])
		})
	}
}

// The bundle holds an amd64 binary, so an arm64 bastion is refused before
// anything is uploaded or changed.
func TestInstallWorker_RefusesABinaryForAnotherPlatform(t *testing.T) {
	t.Parallel()

	bundle, _ := writeWorkerBundle(t, workerJoinDoc(), workerBinary)
	h := newWorkerHarness(t, workerProbe("aarch64", "", ""), false)

	err := h.m.installWorkerFrom(context.Background(), bundleEnv(bundle))
	require.ErrorIs(t, err, errWorkerBinaryPlatform)
	assert.Equal(t, []string{"probe"}, h.mock.steps, "nothing is uploaded or changed")
	assertNoSeed(t, h, err)
}

func TestInstallWorker_RefusesAMismatchBeforeReachingTheBastion(t *testing.T) {
	t.Parallel()

	join := workerJoinDoc()
	join["binary"] = map[string]any{"sha256": map[string]string{"linux/amd64": strings.Repeat("cd", 32)}}
	bundle, _ := writeWorkerBundle(t, join, workerBinary)
	h := newWorkerHarness(t, workerProbe("x86_64", "", ""), false)

	err := h.m.installWorkerFrom(context.Background(), bundleEnv(bundle))
	require.ErrorIs(t, err, workerunit.ErrBundleSum)
	assert.Empty(t, h.mock.steps, "a binary the join file does not list never leaves the workstation")
	assertNoSeed(t, h, err)
}

func TestInstallWorker_RefusesAnotherBlocsBundle(t *testing.T) {
	t.Parallel()

	join := workerJoinDoc()
	join["bloc"] = "ocfp-lab-other"
	bundle, _ := writeWorkerBundle(t, join, workerBinary)
	h := newWorkerHarness(t, workerProbe("x86_64", "", ""), false)

	err := h.m.installWorkerFrom(context.Background(), bundleEnv(bundle))
	require.ErrorIs(t, err, errWorkerBundleBloc)
	assert.Contains(t, err.Error(), "ocfp-lab-other")
	assert.Empty(t, h.mock.steps)
}

func TestInstallWorker_RefusesABastionWithNoEd25519HostKey(t *testing.T) {
	t.Parallel()

	bundle, _ := writeWorkerBundle(t, workerJoinDoc(), workerBinary)
	probe := strings.Replace(workerProbe("x86_64", "", ""), "host_key="+workerHostKey, "host_key=", 1)
	h := newWorkerHarness(t, probe, false)

	err := h.m.installWorkerFrom(context.Background(), bundleEnv(bundle))
	require.ErrorIs(t, err, workerunit.ErrNoHostKey)
	assert.Equal(t, []string{"probe"}, h.mock.steps)
}

func TestInstallWorker_AMissingBundleFails(t *testing.T) {
	t.Parallel()

	h := newWorkerHarness(t, workerProbe("x86_64", "", ""), false)

	err := h.m.installWorkerFrom(context.Background(), bundleEnv(filepath.Join(t.TempDir(), "missing.tar.gz")))
	require.Error(t, err)
	assert.Empty(t, h.mock.steps)
}

func TestInstallWorker_DryRunChecksTheBundleAndChangesNothing(t *testing.T) {
	t.Parallel()

	bundle, _ := writeWorkerBundle(t, workerJoinDoc(), workerBinary)
	h := newWorkerHarness(t, workerProbe("x86_64", "", ""), true)

	require.NoError(t, h.m.installWorkerFrom(context.Background(), bundleEnv(bundle)))
	assert.Empty(t, h.mock.steps, "a dry run reaches nothing on the bastion")
	assert.Positive(t, h.logs.FilterMessageSnippet("DRY RUN").Len())

	join := workerJoinDoc()
	join["joinKey"] = "not-a-seed"
	bad, _ := writeWorkerBundle(t, join, workerBinary)

	err := h.m.installWorkerFrom(context.Background(), bundleEnv(bad))
	require.ErrorIs(t, err, workerunit.ErrJoinFile, "a dry run fails on a bundle the real run would refuse")
}

func TestInstallWorker_AFailedStepReportsItsOutput(t *testing.T) {
	t.Parallel()

	bundle, _ := writeWorkerBundle(t, workerJoinDoc(), workerBinary)
	h := newWorkerHarness(t, workerProbe("x86_64", "", ""), false)
	h.mock.fail = "prepare"

	err := h.m.installWorkerFrom(context.Background(), bundleEnv(bundle))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sudo: a password is required")
	assert.Equal(t, "prepare", h.mock.steps[len(h.mock.steps)-1], "nothing runs after a failed step")
	assertNoSeed(t, h, err)
}

func TestInstallWorker_ConfigNamesTheBundle(t *testing.T) {
	t.Parallel()

	bundle, _ := writeWorkerBundle(t, workerJoinDoc(), workerBinary)
	h := newWorkerHarness(t, workerProbe("x86_64", "", ""), true)
	h.m.config.Bastion.Worker.Bundle = bundle

	require.NoError(t, h.m.installWorkerFrom(context.Background(), bundleEnv("")))
	assert.Positive(t, h.logs.FilterMessageSnippet("DRY RUN").Len(), "the configured bundle is read")
}

func TestDryRunStep_ChecksTheWorkerBundle(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig(workerBloc, "pve"))
	assert.NotNil(t, m.dryRunStep("worker_service"), "a dry run checks the worker bundle")
}
