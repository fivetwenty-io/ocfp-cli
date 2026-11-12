package commands

import (
	"errors"
	"fmt"
	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/fivetwenty-io/ocfp-cli/internal/vault"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
)

// runInitGCP runs `ocfp init gcp` in either form. A dry run makes the same
// checks, including the one that refuses a bloc whose env files exist, and
// logs each write instead of making it.
func runInitGCP(cmd *cobra.Command, r *initRun) error {
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

	r.writesIn(dir)

	for _, slot := range []string{"mgmt", "ocf"} {
		kit := "cf"
		if slot == "mgmt" {
			kit = "bosh"
		}

		opts := vault.WriteEnvFileV32Opts{Path: filepath.Join(dir, "deployments", slot, params.bloc+"-"+slot+".yml"), EnvName: slot, UseCreateEnv: slot == "mgmt", Bloc: params.bloc, IAAS: "gcp", Kit: kit}
		if err := r.change("write the "+slot+" Genesis env file", func() error {
			if err := vault.WriteEnvFileV32Opts_Write(opts); err != nil {
				return fmt.Errorf("write GCP %s environment: %w", slot, err)
			}

			return nil
		}, "path", opts.Path); err != nil {
			return err
		}
	}

	if err := r.writeMonitoringEnvFile(params.bloc, "gcp"); err != nil {
		return err
	}

	return r.persistCurrentBloc(params.bloc)
}
