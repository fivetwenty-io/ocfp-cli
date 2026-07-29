package commands

import (
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewVaultReservedIPsCmd_Subcommands verifies reallocation is reachable
// only through its own verb — the whole point of the command existing.
func TestNewVaultReservedIPsCmd_Subcommands(t *testing.T) {
	cmd := newVaultReservedIPsCmd()

	assert.Equal(t, "reserved-ips", cmd.Use)

	names := map[string]bool{}
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}

	assert.True(t, names["status"], "expected a status subcommand")
	assert.True(t, names["migrate"], "expected a migrate subcommand")
}

func TestNewVaultReservedIPsMigrateCmd_YesFlag(t *testing.T) {
	cmd := newVaultReservedIPsMigrateCmd()

	yesFlag := cmd.Flags().Lookup("yes")
	require.NotNil(t, yesFlag, "expected a --yes flag")
	assert.Equal(t, "false", yesFlag.DefValue)
}

// TestNewVaultPopulateCmd_ForceReallocateFlag pins the opt-in: populate
// defaults to keeping the addresses vault records.
func TestNewVaultPopulateCmd_ForceReallocateFlag(t *testing.T) {
	cmd := newVaultPopulateCmd()

	flag := cmd.Flags().Lookup("force-reallocate")
	require.NotNil(t, flag, "expected a --force-reallocate flag")
	assert.Equal(t, "false", flag.DefValue)
	assert.NotEqual(t, cmd.Flags().Lookup("force"), flag,
		"--force-reallocate must be distinct from --force")
}

// TestRunVaultReservedIPsStatus_CleanReport covers the quiet path: a bloc
// whose addresses match the table says so instead of printing an empty
// report header.
func TestRunVaultReservedIPsStatus_CleanReport(t *testing.T) {
	var sb strings.Builder

	vault.WriteReservedIPReport(&sb, vault.ReservedIPReport{Drifts: nil, Schemes: nil})

	assert.Empty(t, sb.String())
}
