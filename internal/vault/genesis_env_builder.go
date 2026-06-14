package vault

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
)

// GenesisEnvV32 represents a Genesis v3.2 environment file.
// Top-level keys follow the schema validated by Genesis::Env::load().
type GenesisEnvV32 struct {
	Genesis GenesisBlockV32 `yaml:"genesis"`
	Kit     KitBlockV32     `yaml:"kit"`
	// OCFP is pointer-typed so omitempty works correctly.
	// The marshalGenesisEnvV32 helper gates on OCFP.Bloc being non-empty.
	OCFP        *OCFPBlock        `yaml:"ocfp,omitempty"`
	Params      map[string]any    `yaml:"params,omitempty"`
	BOSHConfigs *BOSHConfigsBlock `yaml:"bosh-configs,omitempty"`
}

// GenesisBlockV32 holds the genesis: top-level block.
// vault_path is intentionally absent — removed in v3.2.
type GenesisBlockV32 struct {
	// Env is required; must match the environment file's filename stem.
	Env string `yaml:"env"`
	// UseCreateEnv marks a BOSH proto-director (create-env) deployment.
	// Omitted when false so the key does not appear in the file.
	UseCreateEnv bool `yaml:"use_create_env,omitempty"`
	// MinVersion pins the minimum Genesis version required to deploy this env.
	MinVersion string `yaml:"min_version,omitempty"`
	// SecretsMount overrides the default vault mount for deployment secrets.
	SecretsMount string `yaml:"secrets_mount,omitempty"`
	// SecretsPath overrides the base secrets path resolved by Genesis.
	SecretsPath string `yaml:"secrets_path,omitempty"`
}

// KitBlockV32 holds the kit: top-level block.
// kit.subkits is deprecated; use kit.features.
type KitBlockV32 struct {
	// Name is the kit name (e.g. "bosh", "cf").
	Name string `yaml:"name"`
	// Version is the kit version. "latest" is accepted by Genesis.
	Version string `yaml:"version"`
	// IAAS is required for create-env (proto-BOSH) deployments.
	IAAS string `yaml:"iaas,omitempty"`
	// Features replaces the deprecated kit.subkits.
	Features []string `yaml:"features,omitempty"`
	// Scale is an optional sizing hint ("small", "medium", "large").
	Scale string `yaml:"scale,omitempty"`
}

// OCFPBlock holds the ocfp: top-level block.
// Bloc drives the vault config slug for all OCFP-managed secrets paths.
type OCFPBlock struct {
	// Bloc is required; e.g. "ocfp-aws-us-east-1".
	Bloc string `yaml:"bloc"`
}

// BOSHConfigsBlock holds optional bosh-configs overrides.
// Valid keys are cloud, director_cloud, cpi, and runtime.
// The scale and iaas keys must be under kit:, not here.
type BOSHConfigsBlock struct {
	Cloud         *BOSHCloudConfig `yaml:"cloud,omitempty"`
	DirectorCloud *BOSHCloudConfig `yaml:"director_cloud,omitempty"`
	CPI           *BOSHCloudConfig `yaml:"cpi,omitempty"`
	Runtime       *BOSHCloudConfig `yaml:"runtime,omitempty"`
}

// BOSHCloudConfig holds a reference to a named BOSH cloud/runtime config.
type BOSHCloudConfig struct {
	// Name is the BOSH config name to apply.
	Name string `yaml:"name,omitempty"`
}

// marshalGenesisEnvV32 serializes a GenesisEnvV32 to YAML with a leading
// "---\n" document marker, matching Genesis env file conventions.
// The OCFPBlock is omitted when Bloc is empty.
func marshalGenesisEnvV32(env GenesisEnvV32) ([]byte, error) {
	// Build an ordered MapSlice so key order is deterministic:
	// genesis, kit, ocfp (when present), params (when present), bosh-configs (when present).
	doc := yaml.MapSlice{
		{Key: "genesis", Value: env.Genesis},
		{Key: "kit", Value: env.Kit},
	}

	if env.OCFP != nil && env.OCFP.Bloc != "" {
		doc = append(doc, yaml.MapItem{Key: "ocfp", Value: env.OCFP})
	}

	if len(env.Params) > 0 {
		doc = append(doc, yaml.MapItem{Key: "params", Value: env.Params})
	}

	if env.BOSHConfigs != nil {
		doc = append(doc, yaml.MapItem{Key: "bosh-configs", Value: env.BOSHConfigs})
	}

	encoded, err := yaml.MarshalWithOptions(doc, yaml.Indent(4), yaml.IndentSequence(true))
	if err != nil {
		return nil, fmt.Errorf("yaml marshal: %w", err)
	}

	if !strings.HasPrefix(string(encoded), "---\n") {
		encoded = append([]byte("---\n"), encoded...)
	}

	return encoded, nil
}

// WriteEnvFileV32Opts holds all parameters for writing a Genesis v3.2
// environment file. Use WriteEnvFileV32Opts_Write to execute.
//
// Required fields: Path, EnvName, Bloc, Kit.
// UseCreateEnv=true requires IAAS to be non-empty.
// KitVersion defaults to "latest" when empty.
// MinVersion defaults to "3.2.0" when empty.
type WriteEnvFileV32Opts struct {
	// Path is the destination file path (absolute or relative, non-empty).
	Path string
	// EnvName is the environment stem, e.g. "mgmt" or "ocf". When it does not
	// already start with Bloc+"-", the full genesis.env value is Bloc+"-"+EnvName.
	EnvName string
	// UseCreateEnv marks a BOSH proto-director (create-env) deployment.
	// Requires IAAS to be non-empty.
	UseCreateEnv bool
	// Bloc is the OCFP bloc identifier, e.g. "ocfp-aws-us-east-1". Required.
	Bloc string
	// IAAS is the IaaS name, e.g. "aws". Required when UseCreateEnv is true.
	// Also written to kit.iaas when non-empty.
	IAAS string
	// Kit is the Genesis kit name, e.g. "bosh" or "cf". Required.
	Kit string
	// KitVersion overrides the kit version. Defaults to "latest".
	KitVersion string
	// Features is an optional list of kit features written to kit.features.
	Features []string
	// Scale is an optional sizing hint written to kit.scale (e.g. "dev").
	// Some kits (prometheus, doomsday) read kit.scale to short-circuit the
	// Genesis::Env::scale() director_exodus_lookup recursion.
	Scale string
	// Params is written as the top-level params: block when non-nil and non-empty.
	Params map[string]any
	// BOSHConfigs is written as the top-level bosh-configs: block when non-nil.
	BOSHConfigs *BOSHConfigsBlock
	// MinVersion overrides the genesis.min_version pin. Defaults to "3.2.0".
	// All v3.2 environments must declare a minimum Genesis version regardless
	// of whether they use create-env, so this field is always written.
	MinVersion string
}

// WriteEnvFileV32Opts_Write writes a Genesis v3.2 environment file using the
// options struct. All validation and defaults are applied before writing.
//
// Errors:
//   - path, envName, bloc, kit empty → descriptive error
//   - UseCreateEnv true with empty IAAS → error (Genesis rejects create-env without kit.iaas)
//   - filesystem errors from os.MkdirAll or os.WriteFile
//   - yaml serialization errors (structural; unexpected with well-typed inputs)
func WriteEnvFileV32Opts_Write(opts WriteEnvFileV32Opts) error {
	// --- input validation ---
	if opts.Path == "" {
		return errors.New("path is required for genesis env file")
	}

	if opts.EnvName == "" {
		return errors.New("envName is required for genesis env file")
	}

	if opts.Bloc == "" {
		return errors.New("bloc is required for genesis env file")
	}

	if opts.Kit == "" {
		return errors.New("kit name is required for genesis env file")
	}

	if opts.UseCreateEnv && opts.IAAS == "" {
		return errors.New("kit.iaas required when use_create_env=true")
	}

	// Apply defaults.
	minVersion := opts.MinVersion
	if minVersion == "" {
		minVersion = "3.2.0"
	}

	kitVersion := opts.KitVersion
	if kitVersion == "" {
		kitVersion = "latest"
	}

	// Derive full env name: bloc-envName unless already prefixed.
	prefix := opts.Bloc + "-"

	fullEnvName := opts.EnvName
	if !strings.HasPrefix(opts.EnvName, prefix) {
		fullEnvName = prefix + opts.EnvName
	}

	// Build genesis block. min_version is unconditional for all v3.2 environments.
	genesisBlock := GenesisBlockV32{
		Env:        fullEnvName,
		MinVersion: minVersion,
	}
	if opts.UseCreateEnv {
		genesisBlock.UseCreateEnv = true
	}

	// Build kit block.
	kitBlock := KitBlockV32{
		Name:     opts.Kit,
		Version:  kitVersion,
		IAAS:     opts.IAAS,
		Features: opts.Features,
		Scale:    opts.Scale,
	}

	env := GenesisEnvV32{
		Genesis:     genesisBlock,
		Kit:         kitBlock,
		OCFP:        &OCFPBlock{Bloc: opts.Bloc},
		Params:      opts.Params,
		BOSHConfigs: opts.BOSHConfigs,
	}

	data, err := marshalGenesisEnvV32(env)
	if err != nil {
		return fmt.Errorf("serialize genesis env file: %w", err)
	}

	// Ensure parent directory exists.
	dir := filepath.Dir(opts.Path)
	if err := os.MkdirAll(dir, GenesisDirMode); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	// Write with restricted permissions — env files may reference secrets paths.
	if err := os.WriteFile(opts.Path, data, GenesisFileMode); err != nil {
		return fmt.Errorf("write genesis env file %s: %w", opts.Path, err)
	}

	return nil
}

// WriteEnvFileV32 writes a Genesis v3.2 environment file to path.
// Deprecated: use WriteEnvFileV32Opts_Write with WriteEnvFileV32Opts to pass
// Features, Params, BOSHConfigs, or a custom MinVersion/KitVersion.
//
// genesis.min_version is always set to "3.2.0" for all v3.2 environments,
// regardless of use_create_env.
//
// Parameters:
//   - path        destination path (must be non-empty)
//   - envName     environment stem; prefixed with bloc+"-" when needed
//   - useCreateEnv  when true, sets genesis.use_create_env; requires non-empty iaas
//   - bloc        OCFP bloc identifier (required, e.g. "ocfp-aws-us-east-1")
//   - iaas        IaaS name (e.g. "aws"); required when useCreateEnv is true
//   - kit         name (e.g. "bosh", "cf")
//
// Errors: same as WriteEnvFileV32Opts_Write.
func WriteEnvFileV32(path, envName string, useCreateEnv bool, bloc, iaas, kit string) error {
	return WriteEnvFileV32Opts_Write(WriteEnvFileV32Opts{
		Path:         path,
		EnvName:      envName,
		UseCreateEnv: useCreateEnv,
		Bloc:         bloc,
		IAAS:         iaas,
		Kit:          kit,
	})
}
