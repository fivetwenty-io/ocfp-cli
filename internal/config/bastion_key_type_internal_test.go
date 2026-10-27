package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

const (
	typeCheckEd25519Line = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl wayne@example"
)

// The leading type token of a line must name the type of the key its data
// encodes, whatever the parser itself tolerates.
func TestLiteralKeyTypeTokenMustMatchKeyData(t *testing.T) {
	t.Parallel()

	pub, _, _, _, err := gossh.ParseAuthorizedKey([]byte(typeCheckEd25519Line))
	require.NoError(t, err)

	assert.True(t, literalKeyTypeMatches(typeCheckEd25519Line, pub))
	assert.True(t, literalKeyTypeMatches("\tssh-ed25519   "+strings.Fields(typeCheckEd25519Line)[1], pub))
	assert.False(t, literalKeyTypeMatches("ssh-rsa "+strings.Fields(typeCheckEd25519Line)[1], pub))
	assert.False(t, literalKeyTypeMatches("", pub))
}
