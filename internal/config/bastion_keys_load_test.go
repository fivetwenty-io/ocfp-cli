package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A bloc whose bastion.keys holds a bare name still loads, so every command
// that only reads the bloc keeps working, and loading it prints one warning
// naming the bloc, the entry, and why it cannot be read.
func TestLoadBlocFromFile_WarnsOnABareBastionKeyEntry(t *testing.T) {
	isolateLoadHome(t)

	path := writeLoadConfig(t, "name: barekey\nprovider: pve\n"+fakePVEAuth+
		"bastion:\n  keys:\n    wayne: wayne\n    alice: github/alice\n")

	var (
		cfg *Config
		err error
	)

	stderr := captureStderr(t, func() {
		cfg, err = LoadBlocFromFile(path, "barekey")
	})
	require.NoError(t, err)
	assert.Equal(t, "wayne", cfg.Bastion.Keys["wayne"], "the entry is kept as written")

	assert.Contains(t, stderr, "WARNING")
	assert.Contains(t, stderr, "bloc barekey")
	assert.Contains(t, stderr, "keys.wayne")
	assert.Contains(t, stderr, "names no key source")
	assert.NotContains(t, stderr, "alice")
}
