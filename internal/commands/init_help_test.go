package commands

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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
