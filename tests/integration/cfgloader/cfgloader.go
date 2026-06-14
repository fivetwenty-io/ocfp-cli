// Package cfgloader reads tests/integration/config.yml, validates required keys
// per-tier, resolves secrets via an injected BoshInt function, and synthesizes a
// CPI JSON config map. It is a pure library — no subprocess calls, no build tag.
// The caller (integration harness binary) supplies the BoshInt implementation.
package cfgloader

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

// Tier identifies an integration test tier.
type Tier int

const (
	TierOne Tier = iota + 1
	TierTwo
	TierThree
)

// TiersBlock controls which tiers are enabled in a given run.
type TiersBlock struct {
	Lifecycle bool `yaml:"lifecycle"`
	Bosh      bool `yaml:"bosh"`
	CF        bool `yaml:"cf"`
	Light     bool `yaml:"light"`
}

// SDNCfg holds the SDN sub-block from tier1.network_test.sdn.
type SDNCfg struct {
	Zone     string `yaml:"zone"`
	ZoneType string `yaml:"zone_type"`
	VNet     string `yaml:"vnet"`
	Range    string `yaml:"range"`
	Gateway  string `yaml:"gateway"`
	IP       string `yaml:"ip"`
}

// BridgeCfg holds the bridge sub-block from tier1.network_test.bridge.
type BridgeCfg struct {
	Iface string `yaml:"iface"`
}

// NetworkTestCfg holds the network_test block inside tier1.
type NetworkTestCfg struct {
	Modes  interface{} `yaml:"modes"` // "auto", a list, or absent
	SDN    SDNCfg      `yaml:"sdn"`
	Bridge BridgeCfg   `yaml:"bridge"`
}

// Tier1Config holds all tier1 fields.
type Tier1Config struct {
	VMIDRangeStart       int            `yaml:"vmid_range_start"`
	VMIDRangeEnd         int            `yaml:"vmid_range_end"`
	NetworkIP            string         `yaml:"network_ip"`
	NetworkRange         string         `yaml:"network_range"`
	NetworkGateway       string         `yaml:"network_gateway"`
	NetworkBridge        string         `yaml:"network_bridge"`
	NetworkDNS           []string       `yaml:"network_dns"`
	VMCores              int            `yaml:"vm_cores"`
	VMMemoryMiB          int            `yaml:"vm_memory_mib"`
	DiskSizeMiB          int            `yaml:"disk_size_mib"`
	StemcellPath         string         `yaml:"stemcell_path"`
	SnapshotDetachBypass bool           `yaml:"snapshot_detach_bypass"`
	NetworkTest          NetworkTestCfg `yaml:"network_test"`
}

// Tier2Config holds all tier2 fields.
type Tier2Config struct {
	BoshEnvAlias   string `yaml:"bosh_env_alias"`
	DeploymentName string `yaml:"deployment_name"`
	DeployTimeoutS int    `yaml:"deploy_timeout_s"`
}

// Tier3Config holds all tier3 fields.
type Tier3Config struct {
	DeploymentName string `yaml:"deployment_name"`
	SmokeOrg       string `yaml:"smoke_org"`
	SmokeSpace     string `yaml:"smoke_space"`
	SmokeApp       string `yaml:"smoke_app"`
	SmokeTimeoutS  int    `yaml:"smoke_timeout_s"`
}

// Config is the fully parsed and validated integration config.
type Config struct {
	Tiers     TiersBlock  `yaml:"tiers"`
	BoshVars  string      `yaml:"bosh_vars"`
	BoshCreds string      `yaml:"bosh_creds"`
	CFVars    string      `yaml:"cf_vars"`
	Tier1     Tier1Config `yaml:"tier1"`
	Tier2     Tier2Config `yaml:"tier2"`
	Tier3     Tier3Config `yaml:"tier3"`
}

// BoshInt extracts a value at jsonPath from a bosh-interpolated file.
// Implementations must run `bosh int <file> --path <jsonPath>` and return
// stripped stdout. Injection point for tests.
type BoshInt func(file, jsonPath string) (string, error)

// RequiredTopKeys returns the ordered list of required top-level YAML keys.
// Order is stable and matches the Python _REQUIRED_TOP tuple.
func RequiredTopKeys() []string {
	return []string{"tiers", "bosh_vars", "bosh_creds", "cf_vars", "tier1", "tier2", "tier3"}
}

// RequiredTier1Keys returns the ordered list of required tier1 keys.
// Order matches _REQUIRED_TIER1 in scripts/_integration.py.
func RequiredTier1Keys() []string {
	return []string{
		"vmid_range_start",
		"vmid_range_end",
		"network_ip",
		"network_range",
		"network_gateway",
		"network_bridge",
		"vm_cores",
		"vm_memory_mib",
		"disk_size_mib",
	}
}

// RequiredTier2Keys returns the ordered list of required tier2 keys.
func RequiredTier2Keys() []string {
	return []string{"bosh_env_alias"}
}

// RequiredTier3Keys returns the ordered list of required tier3 keys.
// tier3 has no required keys beyond its presence as a top-level key.
func RequiredTier3Keys() []string {
	return []string{}
}

// Load reads the YAML file at yamlPath, validates all required keys, and
// returns a fully populated *Config.
//
// Inputs:
//   - yamlPath: path to config YAML; must be readable and contain a YAML mapping.
//   - boshInt: injected function for secret resolution; Load does NOT call it —
//     that is the caller's responsibility after Load returns.
//
// Failure modes:
//   - file not found → error naming the path
//   - YAML parse error → error with parse detail
//   - top-level key missing → error naming key and file
//   - tier1 key missing → error naming key with "tier1." prefix and file
//   - tier2 key missing → error naming key with "tier2." prefix and file
//
// boshInt is accepted for interface symmetry with SynthesizeCPIConfig but is
// not invoked inside Load.
func Load(yamlPath string, _ BoshInt) (*Config, error) {
	data, err := os.ReadFile(yamlPath)
	if err != nil {
		return nil, fmt.Errorf("config file not found: %s: %w", yamlPath, err)
	}

	// First unmarshal into a raw map to validate required key presence before
	// we decode into typed structs. YAML may omit a key entirely when its
	// value is zero/null, so we must check the raw map.
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse config: %s: %w", yamlPath, err)
	}

	if raw == nil {
		return nil, fmt.Errorf("config file does not contain a YAML mapping: %s", yamlPath)
	}

	// Validate top-level required keys.
	for _, key := range RequiredTopKeys() {
		if _, ok := raw[key]; !ok {
			return nil, fmt.Errorf("missing required config key: %s  (file: %s)", key, yamlPath)
		}
	}

	// Validate tier1 required keys from the raw map.
	tier1Raw, _ := raw["tier1"].(map[string]interface{})
	if tier1Raw == nil {
		// tier1 key is present but null/wrong type — every required key is missing.
		tier1Raw = map[string]interface{}{}
	}

	for _, key := range RequiredTier1Keys() {
		if _, ok := tier1Raw[key]; !ok {
			return nil, fmt.Errorf("missing required config key: tier1.%s  (file: %s)", key, yamlPath)
		}
	}

	// Validate tier2 required keys.
	tier2Raw, _ := raw["tier2"].(map[string]interface{})
	if tier2Raw == nil {
		tier2Raw = map[string]interface{}{}
	}

	for _, key := range RequiredTier2Keys() {
		if _, ok := tier2Raw[key]; !ok {
			return nil, fmt.Errorf("missing required config key: tier2.%s  (file: %s)", key, yamlPath)
		}
	}

	// Decode into typed struct now that required keys are confirmed present.
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to decode config: %s: %w", yamlPath, err)
	}

	return &cfg, nil
}

// SynthesizeCPIConfig builds the CPI JSON config map from cfg, resolving PVE
// secrets via boshInt calls against cfg.BoshVars.
//
// Inputs:
//   - cfg: validated *Config from Load; must not be nil.
//   - boshInt: must not be nil; called for each required bosh_vars path.
//
// Required bosh_vars paths queried:
//
//	/pve_host, /pve_port, /pve_user, /pve_node, /pve_vm_storage,
//	/pve_disk_storage, /pve_stemcell_storage, /pve_iso_storage,
//	/pve_network_bridge, /pve_verify_ssl, /pve_api_token, /pve_password
//
// Auth preference: pve_api_token wins when non-empty and not a dry-run
// placeholder; otherwise pve_password is used. If both are empty/placeholder,
// both are included in the map (dry-run behaviour).
//
// network_bridge: cfg.Tier1.NetworkBridge takes precedence over
// pve_network_bridge from vars when non-empty.
//
// pve_port: absent/empty → default 8006. Non-integer → error.
//
// verify_ssl is always false for test isolation (matches Python behaviour).
//
// Failure modes:
//   - boshInt returns error for any path → error with path name included
//   - pve_port non-integer → error with raw value included
//   - cfg nil → panic (programming error)
func SynthesizeCPIConfig(cfg *Config, boshInt BoshInt) (map[string]any, error) {
	if cfg == nil {
		panic("cfgloader.SynthesizeCPIConfig: cfg must not be nil")
	}

	if boshInt == nil {
		panic("cfgloader.SynthesizeCPIConfig: boshInt must not be nil")
	}

	varsFile := cfg.BoshVars

	fetch := func(path string) (string, error) {
		val, err := boshInt(varsFile, path)
		if err != nil {
			return "", fmt.Errorf("bosh int failed for path %q in %s: %w", path, varsFile, err)
		}

		return val, nil
	}

	host, err := fetch("/pve_host")
	if err != nil {
		return nil, err
	}

	portRaw, err := fetch("/pve_port")
	if err != nil {
		return nil, err
	}

	user, err := fetch("/pve_user")
	if err != nil {
		return nil, err
	}

	node, err := fetch("/pve_node")
	if err != nil {
		return nil, err
	}

	vmStorage, err := fetch("/pve_vm_storage")
	if err != nil {
		return nil, err
	}

	diskStorage, err := fetch("/pve_disk_storage")
	if err != nil {
		return nil, err
	}

	stemcellStorage, err := fetch("/pve_stemcell_storage")
	if err != nil {
		return nil, err
	}

	isoStorage, err := fetch("/pve_iso_storage")
	if err != nil {
		return nil, err
	}

	networkBridgePVE, err := fetch("/pve_network_bridge")
	if err != nil {
		return nil, err
	}
	// verify_ssl is intentionally fetched but ignored — always false for test isolation.
	if _, err := fetch("/pve_verify_ssl"); err != nil {
		return nil, err
	}

	apiToken, err := fetch("/pve_api_token")
	if err != nil {
		return nil, err
	}

	password, err := fetch("/pve_password")
	if err != nil {
		return nil, err
	}

	// network_bridge: tier1 wins over vars when non-empty.
	networkBridge := strings.TrimSpace(cfg.Tier1.NetworkBridge)
	if networkBridge == "" {
		networkBridge = networkBridgePVE
	}

	// Port coercion: absent/empty → 8006; present non-integer → error.
	port := 8006

	trimmedPort := strings.TrimSpace(portRaw)
	if trimmedPort != "" && !strings.HasPrefix(trimmedPort, "<dry-run:") {
		n := 0
		if _, scanErr := fmt.Sscan(trimmedPort, &n); scanErr != nil || strconv.Itoa(n) != trimmedPort {
			return nil, fmt.Errorf(
				"pve_port value %q in %s is not a valid integer",
				portRaw, varsFile,
			)
		}

		port = n
	}

	cpiCfg := map[string]any{
		"host":             host,
		"port":             port,
		"user":             user,
		"node":             node,
		"vm_storage":       vmStorage,
		"disk_storage":     diskStorage,
		"stemcell_storage": stemcellStorage,
		"iso_storage":      isoStorage,
		"network_bridge":   networkBridge,
		"verify_ssl":       false,
		"vmid_range_start": cfg.Tier1.VMIDRangeStart,
	}

	// Auth: api_token wins when non-empty and not a dry-run placeholder.
	isPlaceholder := func(s string) bool {
		return strings.HasPrefix(s, "<dry-run:")
	}

	if apiToken != "" && !isPlaceholder(apiToken) {
		cpiCfg["api_token"] = apiToken
	} else if password != "" && !isPlaceholder(password) {
		cpiCfg["password"] = password
	} else {
		// dry-run mode: include both placeholder fields.
		cpiCfg["api_token"] = apiToken
		cpiCfg["password"] = password
	}

	return cpiCfg, nil
}
