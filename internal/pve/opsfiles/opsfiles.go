// Package opsfiles embeds PVE-specific BOSH ops files and writes them to the
// bastion deployments ops directory during `ocfp init pve`.
package opsfiles

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed embed/nats-tuning.yml
var NatsTuning string

//go:embed embed/hm-tuning.yml
var HMTuning string

//go:embed embed/os-conf.yml
var OSConf string

//go:embed embed/pve-guest-agent.yml
var PVEGuestAgentRuntimeConfig string

// All returns a map of filename to embedded content for all PVE BOSH ops files.
// Keys are the bare filenames that WriteToDeploymentsDir writes to disk.
func All() map[string]string {
	return map[string]string{
		"nats-tuning.yml": NatsTuning,
		"hm-tuning.yml":   HMTuning,
		"os-conf.yml":     OSConf,
	}
}

// WriteToDeploymentsDir materializes all three PVE BOSH ops files into dir.
// dir is created (with all parents) if it does not exist.
// Each file is written with mode 0644.
// Returns a wrapped error identifying the failing path on any write failure.
func WriteToDeploymentsDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("opsfiles: WriteToDeploymentsDir: dir must not be empty")
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("opsfiles: create directory %q: %w", dir, err)
	}

	for name, content := range All() {
		dest := filepath.Join(dir, name)
		if err := os.WriteFile(dest, []byte(content), 0o600); err != nil {
			return fmt.Errorf("opsfiles: write %q: %w", dest, err)
		}
	}

	return nil
}

// WriteRuntimeConfigToDir writes pve-guest-agent.yml into dir with mode 0644.
// dir is created (with all parents) if it does not exist.
// Returns a wrapped error identifying the failing path on any write failure.
func WriteRuntimeConfigToDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("opsfiles: WriteRuntimeConfigToDir: dir must not be empty")
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("opsfiles: create directory %q: %w", dir, err)
	}

	dest := filepath.Join(dir, "pve-guest-agent.yml")
	if err := os.WriteFile(dest, []byte(PVEGuestAgentRuntimeConfig), 0o600); err != nil {
		return fmt.Errorf("opsfiles: write %q: %w", dest, err)
	}

	return nil
}
