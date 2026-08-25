package bastion

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Phase-list parity: bloc_ca_trust (and every other phase) must be
// registered in both the sequential and the parallel-mode phase lists, or a
// bastion init run in one mode silently skips work the other mode performs.
// ---------------------------------------------------------------------------

// knownParallelOnlyPhaseNames lists phase names that intentionally exist only
// in the parallel-mode phase lists (parallelPrePhaseList/parPhaseList/
// postPhaseList), not in the sequential getInitializationPhases() list.
// ssh_agent_forwarding and bastion_keys run unconditionally in Initialize()
// before the provisioning gate in both modes; the sequential list omits them
// by design (see the comment above getInitializationPhases), while the
// parallel-mode pre-list re-lists them so they still execute first when that
// mode's phase-loop runs standalone (e.g. resumed runs). Any other name
// present in exactly one of the two full phase-name sets is drift, not
// intent, and must fail this test.
var knownParallelOnlyPhaseNames = map[string]bool{
	"ssh_agent_forwarding": true,
	"bastion_keys":         true,
}

// sequentialPhaseNameOrder returns the sequential phase list's names in
// execution order (not just membership), so ordering constraints between
// specific phases can be asserted.
func sequentialPhaseNameOrder(m *Manager) []string {
	phases := m.getInitializationPhases()
	names := make([]string, 0, len(phases))

	for _, p := range phases {
		names = append(names, p.name)
	}

	return names
}

// parallelModePhaseNameOrder returns the parallel-mode phase lists'
// names in execution order: the sequential pre-batch, then the
// (concurrently-run) par-batch, then the sequential post-batch. Phases
// within the par-batch have no relative execution-order guarantee against
// each other (they run concurrently), but pre precedes par precedes post as
// a whole — sufficient for asserting cross-batch ordering constraints like
// vault_populate (post-batch) before bloc_ca_trust (post-batch) before
// ocfp_configure (post-batch).
func parallelModePhaseNameOrder(m *Manager) []string {
	var names []string

	for _, p := range m.parallelPrePhaseList() {
		names = append(names, p.name)
	}

	for _, p := range m.parallelParPhaseList() {
		names = append(names, p.name)
	}

	for _, p := range m.parallelPostPhaseList() {
		names = append(names, p.name)
	}

	return names
}

func sequentialPhaseNameSet(m *Manager) map[string]bool {
	names := make(map[string]bool)

	for _, name := range sequentialPhaseNameOrder(m) {
		names[name] = true
	}

	return names
}

func parallelModePhaseNameSet(m *Manager) map[string]bool {
	names := make(map[string]bool)

	for _, name := range parallelModePhaseNameOrder(m) {
		names[name] = true
	}

	return names
}

// indexOf returns the index of target in names, or -1 if absent.
func indexOf(names []string, target string) int {
	for i, name := range names {
		if name == target {
			return i
		}
	}

	return -1
}

// TestPhaseLists_Parity guards against a phase being registered in only one
// of the two execution-mode phase lists (sequential vs --parallel), which
// silently drops that phase's work whenever the bastion is initialized in
// the other mode. This specifically covers the bloc_ca_trust phase added for
// trust distribution, and generically covers every future phase.
func TestPhaseLists_Parity(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))

	seq := sequentialPhaseNameSet(m)
	par := parallelModePhaseNameSet(m)

	for name := range par {
		if knownParallelOnlyPhaseNames[name] {
			continue
		}

		if !seq[name] {
			t.Errorf("phase %q is registered in the parallel-mode phase lists but missing from the sequential getInitializationPhases() list", name)
		}
	}

	for name := range seq {
		if !par[name] {
			t.Errorf("phase %q is registered in the sequential getInitializationPhases() list but missing from the parallel-mode phase lists", name)
		}
	}
}

// TestPhaseLists_ContainBlocCATrust pins the specific phase this change adds
// so a future edit that removes it from (or forgets to add it to) either
// list fails immediately, independent of the generic diff above.
func TestPhaseLists_ContainBlocCATrust(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))

	assert.True(t, sequentialPhaseNameSet(m)["bloc_ca_trust"], "bloc_ca_trust missing from sequential phase list")
	assert.True(t, parallelModePhaseNameSet(m)["bloc_ca_trust"], "bloc_ca_trust missing from parallel-mode phase lists")
}

// TestPhaseLists_BlocCATrustOrdering pins the ordering constraint that makes
// bloc_ca_trust meaningful: it must run after vault_populate (which may mint
// the bloc CA it distributes) and before ocfp_configure (the first later
// phase that can talk to the artifacts endpoint over TLS). Name-set parity
// alone (TestPhaseLists_Parity) would not catch a future edit that
// re-registers bloc_ca_trust in the right list but the wrong position, or
// that reorders vault_populate/ocfp_configure around it — this test asserts
// the relative order explicitly, in both the sequential and the
// parallel-mode phase lists.
func TestPhaseLists_BlocCATrustOrdering(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))

	assertOrdered := func(t *testing.T, names []string, listLabel string) {
		t.Helper()

		vaultPopulateIdx := indexOf(names, "vault_populate")
		blocCATrustIdx := indexOf(names, "bloc_ca_trust")
		ocfpConfigureIdx := indexOf(names, "ocfp_configure")

		require.GreaterOrEqualf(t, vaultPopulateIdx, 0, "%s: vault_populate not found", listLabel)
		require.GreaterOrEqualf(t, blocCATrustIdx, 0, "%s: bloc_ca_trust not found", listLabel)
		require.GreaterOrEqualf(t, ocfpConfigureIdx, 0, "%s: ocfp_configure not found", listLabel)

		assert.Lessf(t, vaultPopulateIdx, blocCATrustIdx,
			"%s: vault_populate (index %d) must run before bloc_ca_trust (index %d)", listLabel, vaultPopulateIdx, blocCATrustIdx)
		assert.Lessf(t, blocCATrustIdx, ocfpConfigureIdx,
			"%s: bloc_ca_trust (index %d) must run before ocfp_configure (index %d)", listLabel, blocCATrustIdx, ocfpConfigureIdx)
	}

	assertOrdered(t, sequentialPhaseNameOrder(m), "sequential phase list")
	assertOrdered(t, parallelModePhaseNameOrder(m), "parallel-mode phase lists")
}

// ---------------------------------------------------------------------------
// installBlocCATrust skip conditions
// ---------------------------------------------------------------------------

// caTrustMockSSHClient records every command passed to ExecuteCommand so
// tests can assert whether the phase attempted any remote work at all.
type caTrustMockSSHClient struct {
	commands      []string
	checkChecksum string // returned stdout for the sha256sum checksum probe
	installErr    error
	installResult *ssh.CommandResult
}

func (c *caTrustMockSSHClient) Connect(_ context.Context) error { return nil }

func (c *caTrustMockSSHClient) TransferFile(_ context.Context, _, _ string, _ ssh.TransferOptions) error {
	return nil
}

func (c *caTrustMockSSHClient) ExecuteCommand(_ context.Context, cmd string) (*ssh.CommandResult, error) {
	c.commands = append(c.commands, cmd)

	if strings.Contains(cmd, "sha256sum") {
		return &ssh.CommandResult{ExitCode: 0, Stdout: c.checkChecksum}, nil
	}

	if strings.Contains(cmd, "update-ca-certificates") {
		if c.installErr != nil {
			if c.installResult == nil {
				c.installResult = &ssh.CommandResult{ExitCode: 1, Stderr: "boom"}
			}

			return c.installResult, c.installErr
		}

		return &ssh.CommandResult{ExitCode: 0}, nil
	}

	return &ssh.CommandResult{ExitCode: 0}, nil
}

func (c *caTrustMockSSHClient) CreateTunnel(_ context.Context, _, _ int) error { return nil }

func (c *caTrustMockSSHClient) Close() error { return nil }

// artifactsInternalCAConfig returns a base config with artifacts enabled in
// internal-ca TLS mode, for tests that need installBlocCATrust to proceed
// past the mode-gate checks.
func artifactsInternalCAConfig(name string) *config.Config {
	cfg := newBaseConfig(name, "aws")
	cfg.Artifacts.Enabled = true
	cfg.Artifacts.TLS.Mode = config.ArtifactsTLSModeInternalCA

	return cfg
}

func TestInstallBlocCATrust_SkipsWhenArtifactsDisabled(t *testing.T) {
	t.Parallel()

	cfg := newBaseConfig("bloc1", "aws")
	cfg.Artifacts.Enabled = false

	mockSSH := &caTrustMockSSHClient{}
	m := newMinimalManager(cfg)
	m.sshClient = mockSSH

	err := m.installBlocCATrust(context.Background())
	require.NoError(t, err)
	assert.Empty(t, mockSSH.commands, "no SSH commands should run when artifacts is disabled")
}

func TestInstallBlocCATrust_SkipsWhenTLSModeSelfSigned(t *testing.T) {
	t.Parallel()

	cfg := newBaseConfig("bloc1", "aws")
	cfg.Artifacts.Enabled = true
	cfg.Artifacts.TLS.Mode = config.ArtifactsTLSModeSelfSigned

	mockSSH := &caTrustMockSSHClient{}
	m := newMinimalManager(cfg)
	m.sshClient = mockSSH

	err := m.installBlocCATrust(context.Background())
	require.NoError(t, err)
	assert.Empty(t, mockSSH.commands, "no SSH commands should run for self-signed TLS mode")
}

func TestInstallBlocCATrust_SkipsWhenTLSModeDisabled(t *testing.T) {
	t.Parallel()

	cfg := newBaseConfig("bloc1", "aws")
	cfg.Artifacts.Enabled = true
	cfg.Artifacts.TLS.Mode = config.ArtifactsTLSModeDisabled

	mockSSH := &caTrustMockSSHClient{}
	m := newMinimalManager(cfg)
	m.sshClient = mockSSH

	err := m.installBlocCATrust(context.Background())
	require.NoError(t, err)
	assert.Empty(t, mockSSH.commands, "no SSH commands should run for disabled TLS mode")
}

func TestInstallBlocCATrust_SkipsWhenNoCAResolvable(t *testing.T) {
	// Not t.Parallel(): uses t.Setenv, which the testing package forbids
	// combining with parallel subtests.

	// No local state file and no vault environment configured: both CA
	// sources fail, and the phase must be a no-op (not an error).
	tmpHome := t.TempDir()
	t.Setenv("OCFP_HOME", tmpHome)
	t.Setenv("VAULT_ADDR", "")
	t.Setenv("VAULT_TOKEN", "")
	t.Setenv("HOME", tmpHome) // steer ~/.saferc fallback away from the real developer machine

	mockSSH := &caTrustMockSSHClient{}
	m := newMinimalManager(artifactsInternalCAConfig("bloc-no-ca"))
	m.sshClient = mockSSH

	err := m.installBlocCATrust(context.Background())
	require.NoError(t, err, "unresolvable CA must be a no-op, not a hard failure")
	assert.Empty(t, mockSSH.commands, "no SSH commands should run when no CA is resolvable")
}

// ---------------------------------------------------------------------------
// validateCACertPEM
// ---------------------------------------------------------------------------

// generateTestCACertPEM returns a self-signed CA certificate PEM usable as
// valid input to validateCACertPEM / pushCATrustToBastion in tests.
func generateTestCACertPEM(t *testing.T) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048) //nolint:mnd
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "ocfp-test-internal-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour), //nolint:mnd
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	var buf strings.Builder
	require.NoError(t, pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: der}))

	return buf.String()
}

func TestValidateCACertPEM_Valid(t *testing.T) {
	t.Parallel()

	certPEM := generateTestCACertPEM(t)
	assert.NoError(t, validateCACertPEM(certPEM))
}

func TestValidateCACertPEM_Empty(t *testing.T) {
	t.Parallel()

	err := validateCACertPEM("")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidCACertPEM))
}

func TestValidateCACertPEM_NotPEM(t *testing.T) {
	t.Parallel()

	err := validateCACertPEM("this is not a certificate")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidCACertPEM))
}

func TestValidateCACertPEM_WrongBlockType(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	require.NoError(t, pem.Encode(&buf, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("not-really-a-key")}))

	err := validateCACertPEM(buf.String())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidCACertPEM))
}

func TestValidateCACertPEM_CorruptDER(t *testing.T) {
	t.Parallel()

	var buf strings.Builder
	require.NoError(t, pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: []byte("not-valid-der-bytes")}))

	err := validateCACertPEM(buf.String())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidCACertPEM))
}

// ---------------------------------------------------------------------------
// pushCATrustToBastion idempotency
// ---------------------------------------------------------------------------

func TestPushCATrustToBastion_SkipsWhenChecksumMatches(t *testing.T) {
	t.Parallel()

	certPEM := generateTestCACertPEM(t)
	localSum := sha256Hex(certPEM)

	mockSSH := &caTrustMockSSHClient{checkChecksum: localSum}
	m := newMinimalManager(artifactsInternalCAConfig("bloc1"))
	m.sshClient = mockSSH

	err := m.pushCATrustToBastion(context.Background(), certPEM)
	require.NoError(t, err)

	for _, cmd := range mockSSH.commands {
		assert.NotContains(t, cmd, "update-ca-certificates", "matching checksum must skip the install/update-ca-certificates step")
	}
}

func TestPushCATrustToBastion_InstallsWhenChecksumDiffers(t *testing.T) {
	t.Parallel()

	certPEM := generateTestCACertPEM(t)

	mockSSH := &caTrustMockSSHClient{checkChecksum: "deadbeef"}
	m := newMinimalManager(artifactsInternalCAConfig("bloc1"))
	m.sshClient = mockSSH

	err := m.pushCATrustToBastion(context.Background(), certPEM)
	require.NoError(t, err)

	var sawInstall bool

	for _, cmd := range mockSSH.commands {
		if strings.Contains(cmd, "update-ca-certificates") {
			sawInstall = true
		}
	}

	assert.True(t, sawInstall, "mismatched checksum must trigger the install/update-ca-certificates step")
}

func TestPushCATrustToBastion_PropagatesInstallError(t *testing.T) {
	t.Parallel()

	certPEM := generateTestCACertPEM(t)

	mockSSH := &caTrustMockSSHClient{
		checkChecksum: "",
		installErr:    errors.New("remote command failed"),
	}
	m := newMinimalManager(artifactsInternalCAConfig("bloc1"))
	m.sshClient = mockSSH

	err := m.pushCATrustToBastion(context.Background(), certPEM)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "installing bloc CA trust")
}

// sha256Hex mirrors the checksum computation in pushCATrustToBastion so
// tests can construct a "checksum already matches" scenario.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))

	return hex.EncodeToString(sum[:])
}

// localPhaseNameOrder returns the local-executor phase names in execution
// order. `ocfp init bastion` run ON a bastion takes this list, not either of
// the remote ones.
func localPhaseNameOrder(m *Manager) []string {
	le := &LocalExecutor{log: logger.Get()}

	var names []string
	for _, p := range le.getLocalPhases(m) {
		names = append(names, p.name)
	}

	return names
}

// knownRemoteOnlyPhaseNames are phases that legitimately have no local
// counterpart, as opposed to ones that drifted out of the local list.
var knownRemoteOnlyPhaseNames = map[string]string{
	// Transfers the operator's config to the bastion; a local run is already
	// there and reads the file in place.
	"config_files": "copies operator config to a remote bastion",
	// Stops the workstation-local inception vault, which a bastion-side run
	// has no reach into and no business stopping.
	"local_vault_teardown": "acts on the operator workstation",
	// Not a deliberate exclusion. Custom scripts silently do not run in local
	// mode, which is its own bug, but adding execution of operator-defined
	// scripts to a mode that never ran them is a behaviour change rather than
	// a fix. Tracked separately; listed here so parity does not mask it.
	"custom_scripts": "known gap, see comment",
}

// TestPhaseLists_LocalParity extends the sequential-versus-parallel guard
// above to the third list, which had no coverage and had drifted the
// furthest. genesis_secrets_providers was registered in both remote lists and
// absent from the local one, so a bastion-side init pointed safe at the
// inception vault and never pointed it back: every later genesis command then
// reported its secrets missing, with nothing in the log to say why, because
// the phase that restores the target had simply never run.
func TestPhaseLists_LocalParity(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))

	seq := sequentialPhaseNameSet(m)
	local := make(map[string]bool)

	for _, name := range localPhaseNameOrder(m) {
		local[name] = true
	}

	for name := range seq {
		if _, ok := knownRemoteOnlyPhaseNames[name]; ok {
			continue
		}

		if !local[name] {
			t.Errorf("phase %q is registered in the sequential phase list but missing from getLocalPhases(); a bastion-side init silently skips its work", name)
		}
	}
}

// TestLocalPhaseList_Ordering pins the three ordering constraints the local
// list was violating. Each is load-bearing: vault_populate needs the vault
// that vault_inception starts, bloc_ca_trust must converge the trust store
// before anything reaches the artifacts endpoint over TLS, and ocfp_configure
// clones the deployment repositories that genesis_secrets_providers walks.
func TestLocalPhaseList_Ordering(t *testing.T) {
	t.Parallel()

	m := newMinimalManager(newBaseConfig("bloc1", "aws"))
	names := localPhaseNameOrder(m)

	ordered := []string{
		"vault_inception",
		"vault_populate",
		"bloc_ca_trust",
		"ocfp_configure",
		"genesis_secrets_providers",
	}

	for i := 0; i < len(ordered)-1; i++ {
		before, after := ordered[i], ordered[i+1]

		bi, ai := indexOf(names, before), indexOf(names, after)
		if bi == -1 {
			t.Errorf("phase %q missing from getLocalPhases()", before)

			continue
		}

		if ai == -1 {
			t.Errorf("phase %q missing from getLocalPhases()", after)

			continue
		}

		if bi >= ai {
			t.Errorf("getLocalPhases() runs %q (position %d) at or after %q (position %d); it must come first", before, bi, after, ai)
		}
	}
}

// TestLocalExecutor_GenesisOnlyRequested covers the flag that decides between
// a genesis-only update and a full local re-provision. It was not consulted at
// all on the local path, so `ocfp init bastion --genesis` run on a bastion
// accepted the flag, echoed it in the banner, and then ran all 25 phases: the
// flag asked for to narrow the work was what widened it. Options are nil for
// callers taking every default, so that case must not panic.
func TestLocalExecutor_GenesisOnlyRequested(t *testing.T) {
	t.Parallel()

	cfg := newBaseConfig("bloc1", "aws")

	tests := []struct {
		name string
		opts *ProvisioningOptions
		want bool
	}{
		{"nil options take the full path", nil, false},
		{"unset flag takes the full path", &ProvisioningOptions{}, false},
		{"set flag takes the genesis-only path", &ProvisioningOptions{GenesisOnly: true}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			le := NewLocalExecutor(cfg, tc.opts)
			assert.Equal(t, tc.want, le.genesisOnlyRequested())
		})
	}
}
