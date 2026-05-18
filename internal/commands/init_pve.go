package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// validPVEBlocPattern matches PVE bloc names of the form ocfp-pve-<slug> where
// <slug> is one or more lowercase alphanumeric segments separated by dashes.
// Examples accepted: "ocfp-pve-dc1", "ocfp-pve-eu-west-1"
// Examples rejected: "ocfp-aws-us-east-1", "pve-bloc", "ocfp-pve-"
var validPVEBlocPattern = regexp.MustCompile(`^ocfp-pve-[a-z0-9-]+$`)

// resolveInitPVEBloc determines the effective bloc name for `ocfp init pve`.
//
// Resolution order (strict — no state-file or config fallback):
//  1. --bloc flag explicitly passed on the command line (detected via Changed)
//  2. OCFP_BLOC environment variable
//  3. Error — stale viper values from a prior session are NOT accepted
//
// cmd may be nil in isolated unit tests; nil is treated as "flag not provided".
func resolveInitPVEBloc(cmd *cobra.Command) (string, error) {
	if blocFlagChanged(cmd) {
		if v := viper.GetString("bloc"); v != "" {
			return v, nil
		}
	}

	if v := os.Getenv("OCFP_BLOC"); v != "" {
		return v, nil
	}

	return "", ErrBlocMissing
}

// validatePVEBlocName reports whether name satisfies the PVE bloc format.
// Returns ErrBlocFormatInvalid when name does not match.
func validatePVEBlocName(name string) error {
	if !validPVEBlocPattern.MatchString(name) {
		return fmt.Errorf("%w: got %q (PVE blocs must match ^ocfp-pve-[a-z0-9-]+$)", ErrBlocFormatInvalid, name)
	}

	return nil
}

// initPVEParams collects resolved inputs for `ocfp init pve`.
type initPVEParams struct {
	bloc string
}

// resolveInitPVEParams resolves and validates all inputs for `ocfp init pve`.
// Returns a fully-validated initPVEParams or the first error encountered.
// cmd is forwarded to resolveInitPVEBloc for explicit-flag detection.
func resolveInitPVEParams(cmd *cobra.Command) (*initPVEParams, error) {
	bloc, err := resolveInitPVEBloc(cmd)
	if err != nil {
		return nil, err
	}

	if err := validatePVEBlocName(bloc); err != nil {
		return nil, err
	}

	return &initPVEParams{
		bloc: bloc,
	}, nil
}

// initializePVE implements `ocfp init pve`.
//
// cmd must be the cobra command executing `init pve` so that explicit-flag
// detection works correctly. Pass nil only in tests that supply OCFP_BLOC
// instead of --bloc.
//
// It resolves the bloc name (flag > env > error), validates the PVE bloc
// format, persists the bloc as the current bloc in CLI state, and writes two
// minimal v3.2 Genesis env files so that downstream `genesis ocfp` operations
// resolve the correct vault paths:
//
//   - $OCFP_HOME/<bloc>/deployments/mgmt/<bloc>-mgmt.yml  (BOSH director, create-env, iaas=pve)
//   - $OCFP_HOME/<bloc>/deployments/ocf/<bloc>-ocf.yml    (Cloud Foundry, non-create-env)
//
// Config is persisted via config.SetCurrentBloc so that subsequent `ocfp`
// invocations can resolve the bloc without re-supplying the flag.
func initializePVE(cmd *cobra.Command) error {
	log := logger.Get()

	params, err := resolveInitPVEParams(cmd)
	if err != nil {
		return err
	}

	log.Infow("Initializing PVE environment", "bloc", params.bloc)

	if err := persistBlocToState(params.bloc); err != nil {
		return err
	}

	if err := writePVEDeploymentEnvFile(params.bloc, "mgmt", "bosh", true); err != nil {
		return err
	}

	if err := writePVEDeploymentEnvFile(params.bloc, "ocf", "cf", false); err != nil {
		return err
	}

	log.Infow("PVE environment initialized", "bloc", params.bloc)

	return nil
}

// pveDatacenterFromBloc extracts the datacenter segment from a PVE bloc name.
//
// A PVE bloc has the form "ocfp-pve-<datacenter>" where <datacenter> is
// everything after the "ocfp-pve-" prefix (e.g. "dc1", "eu-west-1").
// The caller must have already validated the bloc with validatePVEBlocName,
// which guarantees the prefix is present and the remainder is non-empty.
func pveDatacenterFromBloc(bloc string) string {
	const prefix = "ocfp-pve-"
	return strings.TrimPrefix(bloc, prefix)
}

// writePVEDeploymentEnvFile writes a Genesis v3.2 env file for a single
// deployment under the given bloc.
//
// Parameters:
//   - bloc         OCFP bloc identifier (e.g. "ocfp-pve-dc1")
//   - deployment   deployment slot name: "mgmt" or "ocf"
//   - kit          Genesis kit name: "bosh" for mgmt, "cf" for ocf
//   - useCreateEnv true for BOSH proto-director (create-env) deployments
//
// Path: $OCFP_HOME/<bloc>/deployments/<deployment>/<bloc>-<deployment>.yml
// IAAS is always "pve" for this command.
//
// The ocf env file includes a params block with pve_datacenter set to the
// datacenter segment of the bloc (everything after "ocfp-pve-"). The mgmt
// env file carries no params block, matching the create-env pattern used by
// the AWS bosh kit.
func writePVEDeploymentEnvFile(bloc, deployment, kit string, useCreateEnv bool) error {
	ocfpHome := config.OcfpHome()
	if ocfpHome == "" {
		return fmt.Errorf("cannot determine OCFP home directory: %w", config.ErrOcfpHomeNotFound)
	}

	envFileName := bloc + "-" + deployment + ".yml"
	envFilePath := filepath.Join(ocfpHome, bloc, "deployments", deployment, envFileName)

	const iaas = "pve"

	opts := vault.WriteEnvFileV32Opts{
		Path:         envFilePath,
		EnvName:      deployment,
		UseCreateEnv: useCreateEnv,
		Bloc:         bloc,
		IAAS:         iaas,
		Kit:          kit,
	}

	// The ocf (CF) env carries pve_datacenter in params, analogous to
	// aws_region in the AWS ocf env. The mgmt (BOSH proto-director) env
	// does not carry params, matching the AWS mgmt pattern.
	if deployment == "ocf" {
		opts.Params = map[string]any{
			"pve_datacenter": pveDatacenterFromBloc(bloc),
		}
	}

	if err := vault.WriteEnvFileV32Opts_Write(opts); err != nil {
		return fmt.Errorf("failed to write genesis env file %s: %w", envFilePath, err)
	}

	return nil
}
