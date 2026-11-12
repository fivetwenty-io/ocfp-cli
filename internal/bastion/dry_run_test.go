package bastion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/bastion/deployments"
	"github.com/fivetwenty-io/ocfp-cli/internal/bastion/provision"
	"github.com/fivetwenty-io/ocfp-cli/internal/bastion/ssh"
	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/fivetwenty-io/ocfp-cli/internal/state"
	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// errTestClientRefusesConnect is what the unconnected client returns from
// Connect, because nothing in a dry run may open a connection.
var errTestClientRefusesConnect = errors.New("the test client refuses to connect")

// unconnectedSSHClient stands in for the client a dry run builds and never
// connects. Every remote call fails the way the real unconnected client
// fails, and every one is recorded, so a test can tell whether a run
// reached for the bastion at all.
type unconnectedSSHClient struct {
	mu    sync.Mutex
	calls []string
}

func (c *unconnectedSSHClient) record(call string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.calls = append(c.calls, call)
}

func (c *unconnectedSSHClient) recorded() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]string(nil), c.calls...)
}

func (c *unconnectedSSHClient) Connect(_ context.Context) error {
	c.record("connect")

	return errTestClientRefusesConnect
}

func (c *unconnectedSSHClient) ExecuteCommand(_ context.Context, cmd string) (*ssh.CommandResult, error) {
	c.record("exec: " + cmd)

	return nil, ssh.ErrSSHClientNotConnected
}

func (c *unconnectedSSHClient) TransferFile(_ context.Context, local, remote string, _ ssh.TransferOptions) error {
	c.record("transfer: " + local + " -> " + remote)

	return ssh.ErrSSHClientNotConnected
}

func (c *unconnectedSSHClient) CreateTunnel(_ context.Context, _, _ int) error {
	c.record("tunnel")

	return ssh.ErrSSHClientNotConnected
}

func (c *unconnectedSSHClient) Close() error { return nil }

// keyFetchRecorder stands in for the GitHub and GitLab key lookups, so no
// test reaches the network. It records each account it is asked for and
// answers with one key.
type keyFetchRecorder struct {
	mu     sync.Mutex
	asked  []string
	answer string
}

func (r *keyFetchRecorder) fetch(host string) ssh.KeyFetchFunc {
	return func(_ context.Context, user string) ([]string, error) {
		r.mu.Lock()
		defer r.mu.Unlock()

		r.asked = append(r.asked, host+"/"+user)

		return []string{r.answer}, nil
	}
}

func (r *keyFetchRecorder) accounts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.asked...)
}

// dryRunBloc is a PVE bloc with one direct key and one account on each key
// host, so every pre-mode step has something it would do.
func dryRunBloc(t *testing.T) *config.Config {
	t.Helper()

	cfg := newBaseConfig("drybloc", "pve")
	cfg.Bastion.Keys = map[string]string{
		"laptop": ed25519Line(t, 7, "laptop"),
		"alice":  "github/alice",
		"bob":    "gitlab/bob",
	}

	return cfg
}

// dryRunManager builds the Manager that setupInfrastructure leaves behind,
// with the given client in place of the one it creates, and a log the test
// can read.
func dryRunManager(t *testing.T, cfg *config.Config, opts *ProvisioningOptions,
	client SSHClient,
) (*Manager, *keyFetchRecorder, *observer.ObservedLogs) {
	t.Helper()

	core, logs := observer.New(zap.InfoLevel)
	fetches := &keyFetchRecorder{answer: ed25519Line(t, 9, "fetched")}

	m := &Manager{
		config:            cfg,
		options:           opts,
		progress:          &ProvisioningProgress{Checkpoints: map[string]bool{}},
		sshClient:         client,
		checkpointManager: NewCheckpointManager(cfg),
		errorHandler:      NewErrorHandler(),
		deploymentModes:   deployments.NewResolver(cfg),
		log:               zap.New(core).Sugar(),
		fetchGitHubKeys:   fetches.fetch("github"),
		fetchGitLabKeys:   fetches.fetch("gitlab"),
	}
	m.provConfig = m.loadProvisioningConfig()

	return m, fetches, logs
}

// loggedDryRun returns the dry-run messages the run logged.
func loggedDryRun(logs *observer.ObservedLogs) []string {
	var messages []string

	for _, entry := range logs.FilterMessageSnippet("DRY RUN").All() {
		messages = append(messages, entry.Message)
	}

	return messages
}

// A dry run never connects, so the steps every init runs before its mode,
// the SSH agent forwarding and the authorized_keys sync among them, must log
// what they would do and leave the bastion alone. The key sync used to read
// authorized_keys over the unconnected client and fail the whole run.
func TestDryRunSyncNeverReachesTheBastion(t *testing.T) {
	t.Parallel()

	client := &unconnectedSSHClient{}
	m, fetches, logs := dryRunManager(t, dryRunBloc(t),
		&ProvisioningOptions{DryRun: true, VaultBootUnitOnly: true}, client)

	require.NoError(t, m.syncAndRun(context.Background()))
	assert.Empty(t, client.recorded(), "a dry run must not send anything to the bastion")
	assert.Empty(t, fetches.accounts(), "a dry run must not fetch keys from GitHub or GitLab")

	messages := loggedDryRun(logs)
	assert.Contains(t, messages, "DRY RUN: Would enable SSH agent forwarding on the bastion")
	assert.Contains(t, messages, "DRY RUN: Would ensure the configured keys are in ~/.ssh/authorized_keys on the bastion")

	keyLog := logs.FilterMessageSnippet("authorized_keys").All()
	require.Len(t, keyLog, 1)

	fields := keyLog[0].ContextMap()
	assert.EqualValues(t, 3, fields["entries"])
	assert.EqualValues(t, 1, fields["direct_keys"])
	assert.Equal(t, []any{"github/alice", "gitlab/bob"}, fields["fetched_accounts"])
}

// A dry run still reads every bastion.keys entry, so an entry a real run
// would refuse fails the dry run the same way, before anything is sent.
func TestDryRunKeySyncStillRefusesABadEntry(t *testing.T) {
	t.Parallel()

	cfg := dryRunBloc(t)
	cfg.Bastion.Keys["wayne"] = "wayne"

	client := &unconnectedSSHClient{}
	m, fetches, _ := dryRunManager(t, cfg, &ProvisioningOptions{DryRun: true, VaultBootUnitOnly: true}, client)

	err := m.syncAndRun(context.Background())
	require.ErrorIs(t, err, config.ErrBastionKeySpecInvalid)
	assert.Contains(t, err.Error(), "bastion key sync failed")
	assert.Contains(t, err.Error(), "keys.wayne")
	assert.Empty(t, client.recorded())
	assert.Empty(t, fetches.accounts())
}

// A real run must still reach the bastion for the key sync and fail the
// init when it cannot, rather than skip the sync because the client is not
// connected.
func TestRealRunKeySyncStillNeedsTheBastion(t *testing.T) {
	t.Parallel()

	client := &unconnectedSSHClient{}
	m, fetches, _ := dryRunManager(t, dryRunBloc(t), &ProvisioningOptions{VaultBootUnitOnly: true}, client)

	err := m.syncAndRun(context.Background())
	require.ErrorIs(t, err, ssh.ErrSSHClientNotConnected)
	assert.Contains(t, err.Error(), "bastion key sync failed")
	assert.Contains(t, client.recorded(), "exec: cat ~/.ssh/authorized_keys 2>/dev/null || true",
		"a real run reads authorized_keys on the bastion")
	assert.ElementsMatch(t, []string{"github/alice", "gitlab/bob"}, fetches.accounts(),
		"a real run fetches the configured accounts")
}

// writeDryRunFile writes content to path, making its directory first.
func writeDryRunFile(t *testing.T, path, content string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

// dryRunWorkstation gives the run a workstation of its own: an ocfp home
// holding the bloc's config and bootstrap state, a home directory holding
// the bloc's bastion key pair, and a local ocfp build. It returns the build's
// path.
func dryRunWorkstation(t *testing.T, cfg *config.Config) string {
	t.Helper()

	ocfpHome := t.TempDir()
	home := t.TempDir()

	t.Setenv("OCFP_HOME", ocfpHome)
	t.Setenv("HOME", home)
	t.Setenv("OCFP_CLI_SOURCE", "")
	t.Setenv("OCFP_BINARY_PATH", "")
	t.Setenv("OCFP_CLI_VERSION", "0.1.0")

	configYAML, err := yaml.Marshal(&config.ConfigFile{Blocs: map[string]*config.Config{cfg.Name: cfg}})
	require.NoError(t, err)
	writeDryRunFile(t, filepath.Join(ocfpHome, "config.yml"), string(configYAML))

	stateDir, err := state.GetStateDir(cfg.Name)
	require.NoError(t, err)
	writeDryRunFile(t, filepath.Join(stateDir, cfg.Name+".json"), "{}")

	writeDryRunFile(t, filepath.Join(home, ".ssh", cfg.Name+"-bastion"), "private")
	writeDryRunFile(t, filepath.Join(home, ".ssh", cfg.Name+"-bastion.pub"), "public")

	build := filepath.Join(t.TempDir(), "ocfp-linux-amd64")
	writeDryRunFile(t, build, "binary")

	return build
}

// Every narrow mode must log what it would do in a dry run and send nothing
// to the bastion. --genesis sent its upgrade script, --config read the
// remote home and transferred files, and --ocfp with a local build probed
// the remote checksum and dereferenced the missing result.
func TestDryRunNarrowModesNeverReachTheBastion(t *testing.T) {
	tests := []struct {
		name  string
		opts  ProvisioningOptions
		setup func(t *testing.T, cfg *config.Config, build string)
		want  []string
	}{
		{
			name: "--ocfp from the release",
			opts: ProvisioningOptions{OCFPOnly: true},
			want: []string{"DRY RUN: Would execute script"},
		},
		{
			name: "--ocfp from a local build",
			opts: ProvisioningOptions{OCFPOnly: true},
			setup: func(t *testing.T, _ *config.Config, build string) {
				t.Helper()
				t.Setenv("OCFP_CLI_SOURCE", "local")
				t.Setenv("OCFP_BINARY_PATH", build)
			},
			want: []string{"DRY RUN: Would upload the local ocfp build to the bastion"},
		},
		{
			name: "--genesis from source",
			opts: ProvisioningOptions{GenesisOnly: true},
			want: []string{"DRY RUN: Would build and install Genesis from source on the bastion"},
		},
		{
			name: "--genesis from a binary",
			opts: ProvisioningOptions{GenesisOnly: true},
			setup: func(t *testing.T, cfg *config.Config, _ string) {
				t.Helper()
				cfg.Bastion.ToolOverrides["genesis"] = config.ToolOverride{URL: "https://example.invalid/genesis"}
			},
			want: []string{"DRY RUN: Would download and install Genesis on the bastion"},
		},
		{
			name: "--config",
			opts: ProvisioningOptions{ConfigOnly: true},
			want: []string{
				"DRY RUN: Would copy the ocfp config, filtered to this bloc, to ~/.ocfp/config.yml on the bastion",
				"DRY RUN: Would copy the bloc's bastion SSH keys to ~/.ssh on the bastion",
				"DRY RUN: Would copy the bootstrap state to the bastion",
			},
		},
		{
			name: "--vault-boot-unit",
			opts: ProvisioningOptions{VaultBootUnitOnly: true},
			want: []string{"DRY RUN: Would install and enable the inception vault boot unit"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := dryRunBloc(t)
			build := dryRunWorkstation(t, cfg)

			if tc.setup != nil {
				tc.setup(t, cfg, build)
			}

			opts := tc.opts
			opts.DryRun = true

			client := &unconnectedSSHClient{}
			m, fetches, logs := dryRunManager(t, cfg, &opts, client)

			require.NoError(t, m.syncAndRun(context.Background()))
			assert.Empty(t, client.recorded(), "a dry run must not send anything to the bastion")
			assert.Empty(t, fetches.accounts())

			messages := loggedDryRun(logs)
			for _, want := range tc.want {
				assert.Contains(t, messages, want)
			}
		})
	}
}

// On the bastion itself the client runs commands for real, so a dry run of
// --genesis or of --ocfp with a local build changed the host it was only
// meant to describe. Both must run nothing.
func TestLocalExecutorDryRunNarrowModesRunNothing(t *testing.T) {
	tests := []struct {
		name  string
		opts  ProvisioningOptions
		setup func(t *testing.T, build string)
	}{
		{name: "--genesis", opts: ProvisioningOptions{GenesisOnly: true}},
		{
			name: "--ocfp from a local build",
			opts: ProvisioningOptions{OCFPOnly: true},
			setup: func(t *testing.T, build string) {
				t.Helper()
				t.Setenv("OCFP_CLI_SOURCE", "local")
				t.Setenv("OCFP_BINARY_PATH", build)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			build := dryRunWorkstation(t, dryRunBloc(t))
			if tc.setup != nil {
				tc.setup(t, build)
			}

			opts := tc.opts
			opts.DryRun = true

			mock := &ocfpCLIMockSSHClient{}
			le, m := localModeManager(&opts, mock)

			require.NoError(t, le.run(context.Background(), m))
			assert.Empty(t, mock.commands, "a dry run on the bastion must not run anything")
			assert.Empty(t, mock.transfers)
		})
	}
}

// A real upload whose checksum probe fails gets no result back, and it must
// go on to the upload and report that failure rather than dereference the
// missing result.
func TestUploadOCFPBinarySurvivesAFailedChecksumProbe(t *testing.T) {
	cfg := dryRunBloc(t)
	build := dryRunWorkstation(t, cfg)
	t.Setenv("OCFP_CLI_SOURCE", "local")
	t.Setenv("OCFP_BINARY_PATH", build)

	client := &unconnectedSSHClient{}
	m, _, _ := dryRunManager(t, cfg, &ProvisioningOptions{OCFPOnly: true}, client)

	err := m.uploadOCFPBinary(context.Background())
	require.ErrorIs(t, err, ssh.ErrSSHClientNotConnected)
	assert.Contains(t, err.Error(), "failed to transfer OCFP binary")
	assert.Contains(t, client.recorded(), "transfer: "+build+" -> /tmp/ocfp-upload")
}

// A dry run of the full phase list previews what it would write. It used to
// read each file's current copy over the unconnected client, which always
// failed, so every file was reported as one init would create. The preview
// must leave the bastion alone and say that it did not read it.
func TestDryRunFullPhaseListNeverReachesTheBastion(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		t.Run(fmt.Sprintf("parallel=%t", parallel), func(t *testing.T) {
			cfg := dryRunBloc(t)
			dryRunWorkstation(t, cfg)

			var out bytes.Buffer

			client := &unconnectedSSHClient{}
			m, fetches, logs := dryRunManager(t, cfg,
				&ProvisioningOptions{DryRun: true, Parallel: parallel, ProgressOut: &out}, client)

			require.NoError(t, m.syncAndRun(context.Background()))
			assert.Empty(t, client.recorded(), "a dry run must not send anything to the bastion")
			assert.Empty(t, fetches.accounts())
			assert.Contains(t, loggedDryRun(logs), "DRY RUN: Would execute phase")

			preview := out.String()
			assert.Contains(t, preview, "a dry run does not read the bastion's copies")
			assert.Contains(t, preview, "~ create ~/.tmux.conf (mode 644) unless it is there",
				"the path is the bastion user's, not one under the workstation's home")
			assert.NotContains(t, preview, os.Getenv("HOME"))
			assert.NotContains(t, preview, "+ create", "a file the run never read is not one it would create")
			assert.Contains(t, preview, "~ write /etc/profile.d/ocfp.sh", "the system files are part of the preview")
		})
	}
}

// The preview lists the managed config files and then the system files
// init writes, without reading the bastion for either.
func TestDryRunPreviewShowsConfigAndSystemFiles(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	client := &unconnectedSSHClient{}
	m, _, _ := dryRunManager(t, dryRunBloc(t), &ProvisioningOptions{DryRun: true, ProgressOut: &out}, client)

	m.previewConfigChanges()
	assert.Empty(t, client.recorded(), "the preview must not read the bastion")

	preview := out.String()
	configAt := strings.Index(preview, "configuration files init would write")
	systemAt := strings.Index(preview, "system files init would write")
	require.GreaterOrEqual(t, configAt, 0, preview)
	require.Greater(t, systemAt, configAt, "the system files come after the config files")

	assert.Contains(t, preview, "a dry run does not read the bastion's copies")
	assert.Contains(t, preview, "~ write /etc/profile.d/ocfp.sh")
	assert.Contains(t, preview, "/etc/environment (entries init sets)")
	assert.Contains(t, preview, "+OCFP_BLOC=")
	assert.NotContains(t, preview, "+ create")
}

// The config-file preview lists what the real script writes. The script
// writes an enabled file even when it is empty, and it only sets a mode
// when the file has one.
func TestConfigFilePreviewMatchesTheScript(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	writeConfigFilePreview(&out, []provision.ConfigFile{
		{Name: "empty", Path: "${HOME}/.empty", Enabled: true, Mode: 0o600},
		{Name: "nomode", Path: "$HOME/.nomode", Content: "x", Enabled: true},
		{Name: "kept", Path: "${HOME}/.kept", Content: "x", Enabled: true, CheckExists: true, Mode: 0o644},
		{Name: "off", Path: "${HOME}/.off", Content: "x", Mode: 0o644},
	})

	assert.Equal(t, "~ write ~/.empty (mode 600)\n"+
		"~ write ~/.nomode\n"+
		"~ create ~/.kept (mode 644) unless it is there\n", out.String())
}

// Without a progress writer there is nowhere to print the preview, and the
// dry run must finish rather than write to nothing.
func TestDryRunFullPhaseListWithoutProgressOutput(t *testing.T) {
	cfg := dryRunBloc(t)
	dryRunWorkstation(t, cfg)

	client := &unconnectedSSHClient{}
	m, _, _ := dryRunManager(t, cfg, &ProvisioningOptions{DryRun: true}, client)

	require.NoError(t, m.syncAndRun(context.Background()))
	assert.Empty(t, client.recorded())
}

// seedCheckpoints writes the resume checkpoint an earlier real run of the
// bloc left after it failed, and a stale one from another bloc that a
// finished real run would clean up. It returns the files and their bytes.
func seedCheckpoints(t *testing.T, cfg *config.Config) map[string]string {
	t.Helper()

	dir := checkpointsDir()
	seeds := map[string]CheckpointData{
		"bastion-" + cfg.Name + "-" + cfg.Provider + ".json": {
			BlocName:        cfg.Name,
			Provider:        cfg.Provider,
			StartTime:       time.Now().Add(-time.Hour),
			LastUpdate:      time.Now().Add(-time.Minute),
			TotalSteps:      30,
			CompletedSteps:  2,
			CurrentStep:     "cloudinit_dropins",
			CompletedPhases: map[string]bool{"ssh_agent_forwarding": true, "bastion_keys": true},
			FailedPhases:    map[string]string{"cloudinit_dropins": "the drop-in sync failed"},
			Version:         "1.0",
		},
		"bastion-oldbloc-pve.json": {
			BlocName:   "oldbloc",
			Provider:   "pve",
			StartTime:  time.Now().Add(-30 * 24 * time.Hour),
			LastUpdate: time.Now().Add(-30 * 24 * time.Hour),
			Version:    "1.0",
		},
	}

	files := make(map[string]string, len(seeds))

	for name, seed := range seeds {
		data, err := json.MarshalIndent(seed, "", "  ")
		require.NoError(t, err)
		writeDryRunFile(t, filepath.Join(dir, name), string(data))
		files[name] = string(data)
	}

	return files
}

// readCheckpoints returns every file in the checkpoints directory and its
// bytes.
func readCheckpoints(t *testing.T) map[string]string {
	t.Helper()

	dir := checkpointsDir()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	files := make(map[string]string, len(entries))

	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, err)

		files[entry.Name()] = string(data)
	}

	return files
}

// A dry run must leave the checkpoints on disk as it found them. The
// parallel workers saved a checkpoint for each step they only logged, and
// the end of the run cleared the bloc's checkpoint and removed old ones,
// so a dry run wiped the resume point of an earlier failed real run.
func TestDryRunLeavesCheckpointsAlone(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		for _, resume := range []bool{false, true} {
			t.Run(fmt.Sprintf("parallel=%t,resume=%t", parallel, resume), func(t *testing.T) {
				cfg := dryRunBloc(t)
				dryRunWorkstation(t, cfg)

				seeded := seedCheckpoints(t, cfg)

				client := &unconnectedSSHClient{}
				m, _, _ := dryRunManager(t, cfg,
					&ProvisioningOptions{DryRun: true, Parallel: parallel, Resume: resume}, client)

				if resume {
					checkpoint, err := m.checkpointManager.Load()
					require.NoError(t, err)
					require.NotNil(t, checkpoint)

					m.progress = m.checkpointManager.RestoreProgress(checkpoint)
				}

				require.NoError(t, m.syncAndRun(context.Background()))
				assert.Empty(t, client.recorded())
				assert.Equal(t, seeded, readCheckpoints(t), "a dry run must not change the checkpoints")
			})
		}
	}
}

// A real run that finishes still clears the bloc's resume checkpoint and
// removes checkpoints more than a week old.
func TestFinishedRealRunClearsCheckpoints(t *testing.T) {
	cfg := dryRunBloc(t)
	dryRunWorkstation(t, cfg)
	seedCheckpoints(t, cfg)

	m, _, _ := dryRunManager(t, cfg, &ProvisioningOptions{}, &unconnectedSSHClient{})

	m.clearCheckpoints()
	assert.Empty(t, readCheckpoints(t))
}
