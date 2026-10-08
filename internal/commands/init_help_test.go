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
