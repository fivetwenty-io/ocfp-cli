package bastion

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// managerWithMockSSH wires a manager to a recording SSH client so tests can
// assert on what provisioning actually sends to the bastion.
func managerWithMockSSH(provider string) (*Manager, *mockSSHClient) {
	cfg := newBaseConfig("bloc1", provider)
	m := newMinimalManager(cfg)
	m.options = &ProvisioningOptions{}

	mock := &mockSSHClient{}
	m.sshClient = mock

	return m, mock
}

// executedScripts joins everything the mock was asked to run, which is where
// the generated script bodies end up.
func executedScripts(mock *mockSSHClient) string {
	return strings.Join(mock.executedCommands, "\n")
}

// The link has to be emitted by the phase that real provisioning runs, not
// only by the standalone script generator.
func TestInstallBrewPackages_EmitsGraftSpruceLink(t *testing.T) {
	m, mock := managerWithMockSSH("aws")

	require.NoError(t, m.installBrewPackages(context.Background()))

	got := executedScripts(mock)
	assert.Contains(t, got, "Link graft as spruce")
	assert.Contains(t, got, "/usr/local/bin/spruce")
}

// The link points at graft, so it has to be created after the cask that
// provides graft, or the install would overwrite it.
func TestInstallBrewPackages_LinksAfterInstall(t *testing.T) {
	m, mock := managerWithMockSSH("aws")

	require.NoError(t, m.installBrewPackages(context.Background()))

	got := executedScripts(mock)
	installIdx := strings.Index(got, "fivetwenty-io/tap/graft")
	linkIdx := strings.Index(got, "Link graft as spruce")

	require.NotEqual(t, -1, installIdx, "graft install missing")
	require.NotEqual(t, -1, linkIdx, "graft link missing")
	assert.Less(t, installIdx, linkIdx, "graft must be installed before it is linked as spruce")
}

// pmx is a PVE-only tool, and the PVE bastion is the only one that should
// carry the tap install for it.
func TestInstallBrewPackages_PMXOnlyOnPVE(t *testing.T) {
	pveManager, pveMock := managerWithMockSSH("pve")
	require.NoError(t, pveManager.installBrewPackages(context.Background()))
	assert.Contains(t, executedScripts(pveMock), "fivetwenty-io/tap/pmx")

	awsManager, awsMock := managerWithMockSSH("aws")
	require.NoError(t, awsManager.installBrewPackages(context.Background()))
	assert.NotContains(t, executedScripts(awsMock), "pmx")
}

// Verification has to match what was installed: pmx is expected on PVE and
// nowhere else, and graft only when it was not disabled.
func TestVerificationTools(t *testing.T) {
	pveManager, _ := managerWithMockSSH("pve")
	pveTools := pveManager.verificationTools()
	assert.Contains(t, pveTools, "pmx")
	assert.Contains(t, pveTools, "graft")
	assert.Contains(t, pveTools, "spruce")

	awsManager, _ := managerWithMockSSH("aws")
	assert.NotContains(t, awsManager.verificationTools(), "pmx")
}

// Disabling graft is a supported opt-out, so verification must stop demanding
// it, while `spruce` stays required because the upstream binary backs it.
func TestVerificationTools_GraftDisabled(t *testing.T) {
	m, _ := managerWithMockSSH("aws")
	m.config.Bastion.Brews.Disable = []string{"graft"}

	tools := m.verificationTools()
	assert.NotContains(t, tools, "graft")
	assert.Contains(t, tools, "spruce")
}

// With graft opted out, something still has to answer to `spruce`, so the
// upstream binary is linked in its place.
func TestInstallBrewPackages_FallsBackToUpstreamSpruce(t *testing.T) {
	m, mock := managerWithMockSSH("aws")
	m.config.Bastion.Brews.Disable = []string{"graft"}

	require.NoError(t, m.installBrewPackages(context.Background()))

	got := executedScripts(mock)
	assert.Contains(t, got, "/usr/local/bin/spruce-orig /usr/local/bin/spruce")
	assert.NotContains(t, got, "Link graft as spruce")
}

// jq has to be findable by systemd units, so the brew phase converges the
// Homebrew jq link right after the spruce link, for every provider.
func TestInstallBrewPackages_EmitsJQBrewLinkAfterSpruceLink(t *testing.T) {
	for _, provider := range []string{"aws", "gcp", "pve"} {
		m, mock := managerWithMockSSH(provider)

		require.NoError(t, m.installBrewPackages(context.Background()))

		got := executedScripts(mock)
		spruceIdx := strings.Index(got, "Link graft as spruce")
		jqIdx := strings.Index(got, "/usr/local/bin/jq")

		require.NotEqual(t, -1, spruceIdx, provider)
		require.NotEqual(t, -1, jqIdx, "%s: jq brew link missing", provider)
		assert.Less(t, spruceIdx, jqIdx, provider)
	}
}

// A failing link must warn and let the rest of init carry on.
func TestInstallBrewPackages_JQBrewLinkFailureIsSoft(t *testing.T) {
	m, _ := managerWithMockSSH("aws")
	m.sshClient = &failOnJQSSHClient{}

	require.NoError(t, m.installBrewPackages(context.Background()))
}

// Bastions that are already provisioned take the fast path, which never runs
// the brew phase, so the converge has to be callable on its own and soft.
func TestConvergeJQBrewLink_FailureDoesNotAbortInit(t *testing.T) {
	m, _ := managerWithMockSSH("pve")
	m.sshClient = &failOnJQSSHClient{}

	require.Error(t, m.convergeJQBrewLink(context.Background()))
	require.NoError(t, m.syncJQBrewLink(context.Background()))
}

func TestConvergeJQBrewLink_RunsOnEveryProviderAndSkipsDryRun(t *testing.T) {
	for _, provider := range []string{"aws", "gcp", "pve"} {
		m, mock := managerWithMockSSH(provider)

		require.NoError(t, m.convergeJQBrewLink(context.Background()))
		assert.Contains(t, executedScripts(mock), "/usr/local/bin/jq", provider)
	}

	m, mock := managerWithMockSSH("aws")
	m.options.DryRun = true

	require.NoError(t, m.convergeJQBrewLink(context.Background()))
	assert.Empty(t, mock.executedCommands)
}
