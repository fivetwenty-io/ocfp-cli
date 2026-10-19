package commands

import (
	"errors"
	"fmt"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
)

func initializeGCP(cmd *cobra.Command) error {
	params, err := resolveInitAWSParams(cmd)
	if err != nil {
		return err
	}

	dir, err := config.OcfpBlocDir(params.bloc)
	if err != nil {
		return err
	}
	// Validate every destination before writing any environment, preserving
	// operator edits and avoiding a partially refreshed deployment tree.
	for _, slot := range []string{"mgmt", "ocf", "prometheus"} {
		env := "ocf"
		if slot == "mgmt" {
			env = "mgmt"
		}

		path := filepath.Join(dir, "deployments", slot, params.bloc+"-"+env+".yml")
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("GCP environment already exists: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}

	for _, slot := range []string{"mgmt", "ocf"} {
		kit := "cf"
		if slot == "mgmt" {
			kit = "bosh"
		}

		opts := vault.WriteEnvFileV32Opts{Path: filepath.Join(dir, "deployments", slot, params.bloc+"-"+slot+".yml"), EnvName: slot, UseCreateEnv: slot == "mgmt", Bloc: params.bloc, IAAS: "gcp", Kit: kit}
		if err := vault.WriteEnvFileV32Opts_Write(opts); err != nil {
			return fmt.Errorf("write GCP %s environment: %w", slot, err)
		}
	}

	if err := writeMonitoringEnvFile(params.bloc, "gcp"); err != nil {
		return err
	}

	return persistBlocToState(params.bloc)
}
