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

	vault.WriteReservedIPReport(&sb, vault.ReservedIPReport{Drifts: nil, Schemes: nil, Obsoletes: nil})

	assert.Empty(t, sb.String())
}

// TestNewVaultPopulateCmd_FQDNFilterFlags pins --plane and --key as
// repeatable flags that default to everything.
func TestNewVaultPopulateCmd_FQDNFilterFlags(t *testing.T) {
	cmd := newVaultPopulateCmd()

	for _, name := range []string{"plane", "key"} {
		flag := cmd.Flags().Lookup(name)
		require.NotNil(t, flag, "expected a --%s flag", name)
		assert.Equal(t, "stringSlice", flag.Value.Type())
		assert.Equal(t, "[]", flag.DefValue)
	}

	require.NoError(t, cmd.Flags().Parse([]string{"--plane", "mgmt", "--plane", "ocf", "--key", "shield,prometheus"}))

	planes, err := cmd.Flags().GetStringSlice("plane")
	require.NoError(t, err)
	assert.Equal(t, []string{"mgmt", "ocf"}, planes)

	keys, err := cmd.Flags().GetStringSlice("key")
	require.NoError(t, err)
	assert.Equal(t, []string{"shield", "prometheus"}, keys)
}

// TestNewVaultPopulateCmd_FQDNFilterValidatedBeforeVault runs the command with
// no bloc configured. A usage error has to win over the missing bloc, which
// proves the check runs before any config or vault access.
func TestNewVaultPopulateCmd_FQDNFilterValidatedBeforeVault(t *testing.T) {
	t.Setenv("OCFP_BLOC", "")

	tests := []struct {
		name    string
		args    []string
		wantErr error
	}{
		{"plane on another phase", []string{"public-ips", "--plane", "mgmt"}, vault.ErrFQDNFilterWrongPhase},
		{"key on the full populate", []string{"--key", "shield"}, vault.ErrFQDNFilterWrongPhase},
		{"unknown plane", []string{"fqdns", "--plane", "prod"}, vault.ErrUnknownFQDNPlane},
		{"empty key", []string{"fqdns", "--force", "--key", ""}, vault.ErrFQDNFilterEmptyValue},
		{"empty plane", []string{"fqdns", "--force", "--plane", ""}, vault.ErrFQDNFilterEmptyValue},
		{"empty key equals form", []string{"fqdns", "--key="}, vault.ErrFQDNFilterEmptyValue},
		{"whitespace key", []string{"fqdns", "--key", "  "}, vault.ErrFQDNFilterEmptyValue},
		{"whitespace plane", []string{"fqdns", "--plane", " "}, vault.ErrFQDNFilterEmptyValue},
		{"trailing comma in key", []string{"fqdns", "--key", "shield,"}, vault.ErrFQDNFilterEmptyValue},
		{"leading comma in plane", []string{"fqdns", "--plane", ",ocf"}, vault.ErrFQDNFilterEmptyValue},
		{"empty key on public-ips", []string{"public-ips", "--key", ""}, vault.ErrFQDNFilterEmptyValue},
		{"empty plane on reserved-ips", []string{"reserved-ips", "--plane", ""}, vault.ErrFQDNFilterEmptyValue},
		{"empty key on the full populate", []string{"--key", ""}, vault.ErrFQDNFilterEmptyValue},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newVaultPopulateCmd()
			cmd.SetArgs(tt.args)
			cmd.SetOut(&strings.Builder{})
			cmd.SetErr(&strings.Builder{})

			require.ErrorIs(t, cmd.Execute(), tt.wantErr)
		})
	}
}
