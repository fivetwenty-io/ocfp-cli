package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// validBlocPattern matches bloc names of the form [a-z0-9][a-z0-9-]*[a-z0-9].
// Single-character names are rejected because the pattern requires at least one
// interior character (so the first and last chars cannot overlap).
// Examples accepted: "ocfp-aws-us-east-1", "my-bloc", "prod"
// Examples rejected: "Foo", "-bar", "bar-", "a", "bad name!"
var validBlocPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*[a-z0-9]$`)

// ErrBlocFormatInvalid is returned when the resolved bloc value does not match
// the required format (lowercase alphanumeric + internal dashes, min length 2).
var ErrBlocFormatInvalid = errors.New(
	"--bloc value must match ^[a-z0-9][a-z0-9-]*[a-z0-9]$ " +
		"(lowercase alphanumeric and dashes only, must start and end with alphanumeric)",
)

// ErrBlocMissing is returned when neither --bloc flag nor OCFP_BLOC env var supplies a bloc name.
var ErrBlocMissing = errors.New(
	"bloc name is required: supply --bloc <name> or set OCFP_BLOC environment variable",
)

// blocFlagChanged reports whether the --bloc persistent flag was explicitly
// provided on the command line.  It walks up to the root command so the check
// works regardless of which subcommand in the hierarchy cmd refers to.
// Returns false when cmd is nil (no cobra context, e.g. direct unit-test calls
// that pass nil — those callers must supply OCFP_BLOC instead).
func blocFlagChanged(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}

	root := cmd.Root()
	if f := root.PersistentFlags().Lookup("bloc"); f != nil {
		return f.Changed
	}

	return false
}

// resolveInitAWSBloc determines the effective bloc name for `ocfp init aws`.
//
// Resolution order (strict — no state-file or config fallback):
//  1. --bloc flag explicitly passed on the command line (detected via Changed)
//  2. OCFP_BLOC environment variable
//  3. Error — stale viper values from a prior session are NOT accepted
//
// cmd may be nil in isolated unit tests; nil is treated as "flag not provided".
func resolveInitAWSBloc(cmd *cobra.Command) (string, error) {
	// Explicit --bloc flag wins.  Read via viper so the binding in root.go
	// (viper.BindPFlag) is honoured; Changed() guards against stale values.
	if blocFlagChanged(cmd) {
		if v := viper.GetString("bloc"); v != "" {
			return v, nil
		}
	}

	// OCFP_BLOC env var is always explicit by nature.
	if v := os.Getenv("OCFP_BLOC"); v != "" {
		return v, nil
	}

	return "", ErrBlocMissing
}

// validateBlocName reports whether name satisfies the allowed format.
// Returns ErrBlocFormatInvalid when name does not match.
func validateBlocName(name string) error {
	if !validBlocPattern.MatchString(name) {
		return fmt.Errorf("%w: got %q", ErrBlocFormatInvalid, name)
	}

	return nil
}

// initAWSParams collects resolved inputs for `ocfp init aws`.
type initAWSParams struct {
	bloc string
}

// resolveInitAWSParams resolves and validates all inputs for `ocfp init aws`.
// Returns a fully-validated initAWSParams or the first error encountered.
// cmd is forwarded to resolveInitAWSBloc for explicit-flag detection.
func resolveInitAWSParams(cmd *cobra.Command) (*initAWSParams, error) {
	bloc, err := resolveInitAWSBloc(cmd)
	if err != nil {
		return nil, err
	}

	if err := validateBlocName(bloc); err != nil {
		return nil, err
	}

	return &initAWSParams{bloc: bloc}, nil
}

// initializeAWS implements `ocfp init aws`.
//
// cmd must be the cobra command executing `init aws` so that explicit-flag
// detection works correctly.  Pass nil only in tests that supply OCFP_BLOC
// instead of --bloc.
//
// It resolves the bloc name (flag > env > error), validates the format, persists
// the bloc as the current bloc in CLI state, and writes two minimal v3.2 Genesis
// env files so that downstream `genesis ocfp` operations resolve the correct
// vault paths:
//
//   - $OCFP_HOME/<bloc>/deployments/mgmt/<bloc>-mgmt.yml  (BOSH director, create-env)
//   - $OCFP_HOME/<bloc>/deployments/ocf/<bloc>-ocf.yml    (Cloud Foundry, non-create-env)
//
// Config is persisted via config.SetCurrentBloc so that subsequent `ocfp`
// invocations can resolve the bloc without re-supplying the flag.
func initializeAWS(cmd *cobra.Command) error {
	log := logger.Get()

	params, err := resolveInitAWSParams(cmd)
	if err != nil {
		return err
	}

	log.Infow("Initializing AWS environment", "bloc", params.bloc)

	if err := persistBlocToState(params.bloc); err != nil {
		return err
	}

	if err := writeAWSDeploymentEnvFile(params.bloc, "mgmt", "bosh", true); err != nil {
		return err
	}

	if err := writeAWSDeploymentEnvFile(params.bloc, "ocf", "cf", false); err != nil {
		return err
	}

	log.Infow("AWS environment initialized", "bloc", params.bloc)

	return nil
}

// persistBlocToState sets the current bloc in CLI state.
// configFile is intentionally left empty — init aws does not require a
// pre-existing OCFP config file; the bloc name alone is the identity.
func persistBlocToState(bloc string) error {
	configFile := viper.GetString("config")

	if err := config.SetCurrentBloc(bloc, configFile); err != nil {
		return fmt.Errorf("failed to persist bloc to state: %w", err)
	}

	return nil
}

// writeAWSDeploymentEnvFile writes a Genesis v3.2 env file for a single
// deployment under the given bloc.
//
// Parameters:
//   - bloc         OCFP bloc identifier (e.g. "ocfp-aws-us-east-1")
//   - deployment   deployment slot name: "mgmt" or "ocf"
//   - kit          Genesis kit name: "bosh" for mgmt, "cf" for ocf
//   - useCreateEnv true for BOSH proto-director (create-env) deployments
//
// Path: $OCFP_HOME/<bloc>/deployments/<deployment>/<bloc>-<deployment>.yml
// IAAS is always "aws" for this command.
func writeAWSDeploymentEnvFile(bloc, deployment, kit string, useCreateEnv bool) error {
	ocfpHome := config.OcfpHome()
	if ocfpHome == "" {
		return fmt.Errorf("cannot determine OCFP home directory: %w", config.ErrOcfpHomeNotFound)
	}

	envFileName := bloc + "-" + deployment + ".yml"
	envFilePath := filepath.Join(ocfpHome, bloc, "deployments", deployment, envFileName)

	const iaas = "aws"

	if err := vault.WriteEnvFileV32(envFilePath, deployment, useCreateEnv, bloc, iaas, kit); err != nil {
		return fmt.Errorf("failed to write genesis env file %s: %w", envFilePath, err)
	}

	return nil
}

