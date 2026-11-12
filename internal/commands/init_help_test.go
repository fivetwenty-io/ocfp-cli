package commands

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The help must not promise that every mode works on the bastion, because
// --config is refused there.
func TestInitLongDescription_ExceptsConfigFromSameWorkClaim(t *testing.T) {
	long := strings.Join(strings.Fields(getInitLongDescription()), " ")
	assert.Contains(t, long, "except --config")
	assert.NotContains(t, long, "it does the same work from a workstation as on the bastion itself.")
}

// The help says what a dry run checks and changes for every component, and
// how the config file asks for one.
func TestInitLongDescription_DescribesTheDryRun(t *testing.T) {
	long := strings.Join(strings.Fields(getInitLongDescription()), " ")

	for _, want := range []string{
		"--dry-run, or init.dry_run: true in the config file",
		"--dry-run=false",
		"refuses wherever the real run would refuse",
		"logs each change instead of making it",
		"never connects to the bastion",
		"logs each check it could not make",
		"A bastion dry run still asks the provider for the bastion's address",
	} {
		assert.Contains(t, long, want)
	}

	usage := NewInitCmd().Flags().Lookup("dry-run").Usage
	assert.NotEqual(t, "print actions without executing", usage)
	assert.Contains(t, usage, "change nothing")
}

// The help states the bloc-name rule the code enforces for aws, gcp, and
// pve, and does not claim a naming scheme that nothing checks.
func TestInitLongDescription_StatesTheBlocNameRule(t *testing.T) {
	long := strings.Join(strings.Fields(getInitLongDescription()), " ")

	assert.NotContains(t, long, "requires --bloc ocfp-aws-<region>")
	assert.Contains(t, long, "aws - Initialize AWS environment (requires --bloc <name>, or OCFP_BLOC)")
	assert.Contains(t, long, "A bloc name for aws, gcp, and pve is lowercase letters, digits, and dashes,")
	assert.Contains(t, long, "at least two characters long, and neither starts nor ends with a dash.")

	// The rule in the help is the rule the code enforces.
	for _, name := range []string{"ocfp-aws-us-east-1", "my-bloc", "prod", "b2"} {
		require.NoError(t, validateBlocName(name), name)
	}

	for _, name := range []string{"Foo", "-bar", "bar-", "a", "bad name!"} {
		require.ErrorIs(t, validateBlocName(name), ErrBlocFormatInvalid, name)
	}
}
