package bastion

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/bastion/deployments"
	"github.com/fivetwenty-io/ocfp-cli/internal/bastion/ssh"
	"github.com/fivetwenty-io/ocfp-cli/internal/config"
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
