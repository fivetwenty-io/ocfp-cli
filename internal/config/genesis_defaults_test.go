package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestApplyGenesisDefaults_Repo pins the default Genesis source. A config
// that names neither genesis.repo nor bastion.genesis.repo builds from the
// RubidiumStudios line, and an explicit repo on either block is kept.
func TestApplyGenesisDefaults_Repo(t *testing.T) {
	t.Parallel()

	t.Run("global defaults", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		applyGenesisDefaults(cfg)

		assert.Equal(t, DefaultGenesisRepo, cfg.Genesis.Repo)
		assert.Equal(t, DefaultGenesisBranch, cfg.Genesis.Branch)
		assert.Equal(t, DefaultGenesisVersionPrefix, cfg.Genesis.VersionPrefix)
		assert.Contains(t, DefaultGenesisRepo, "RubidiumStudios/genesis")
	})

	t.Run("global override kept", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Genesis: Genesis{Enabled: true, Repo: "git@github.com:myorg/genesis"}}
		applyGenesisDefaults(cfg)

		assert.Equal(t, "git@github.com:myorg/genesis", cfg.Genesis.Repo)
	})

	t.Run("bastion defaults and override", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.Bastion.Genesis.Enabled = true
		applyBastionGenesisDefaults(cfg)
		assert.Equal(t, DefaultGenesisRepo, cfg.Bastion.Genesis.Repo)

		cfg = &Config{}
		cfg.Bastion.Genesis.Enabled = true
		cfg.Bastion.Genesis.Repo = "git@github.com:myorg/genesis"
		applyBastionGenesisDefaults(cfg)
		assert.Equal(t, "git@github.com:myorg/genesis", cfg.Bastion.Genesis.Repo)
	})
}
