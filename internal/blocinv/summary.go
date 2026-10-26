package blocinv

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

const (
	// reservedOutputPrefix starts the name of every reserved-IP output in
	// state.
	reservedOutputPrefix = "reserved_"

	// reservedOutputSampleSize caps how many reserved output names a summary
	// lists.
	reservedOutputSampleSize = 5
)

// sensitiveNameParts are fragments that mark an output name as one whose
// value is likely a credential. A summary never lists such a name, even among
// the reserved outputs.
var sensitiveNameParts = []string{"key", "secret", "token", "password", "credential", "cert"}

// Summary is the safe, local view of one bloc that `bloc show` prints.
type Summary struct {
	Config *ConfigSummary `json:"config,omitempty"`
	State  *StateSummary  `json:"state,omitempty"`
}

// ConfigSummary holds the config fields on the allowlist. A field added to the
// config later stays hidden until someone adds it here on purpose.
type ConfigSummary struct {
	File             string `json:"file"`
	Provider         string `json:"provider"`
	IaaS             string `json:"iaas,omitempty"`
	Region           string `json:"region"`
	ProjectID        string `json:"project_id,omitempty"`
	OrgID            string `json:"org_id,omitempty"`
	NetworkCIDR      string `json:"network_cidr"`
	ArtifactsEnabled bool   `json:"artifacts_enabled"`
}

// StateSummary condenses a bloc's state file. It holds counts and a few names,
// never output values other than the bastion and network facts below.
type StateSummary struct {
	Provider             string         `json:"provider"`
	Region               string         `json:"region"`
	CreatedAt            time.Time      `json:"created_at,omitzero"`
	UpdatedAt            time.Time      `json:"updated_at,omitzero"`
	Bastion              BastionSummary `json:"bastion"`
	Network              NetworkSummary `json:"network"`
	Resources            map[string]int `json:"resources"`
	OutputCount          int            `json:"output_count"`
	ReservedOutputCount  int            `json:"reserved_output_count"`
	ReservedOutputSample []string       `json:"reserved_output_sample"`
}

// BastionSummary holds the bastion's id and addresses from state.
type BastionSummary struct {
	ID        string `json:"id"`
	PrivateIP string `json:"private_ip"`
	PublicIP  string `json:"public_ip"`
	SSHHost   string `json:"ssh_host"`
}

// NetworkSummary holds the bloc network's name and CIDR from state.
type NetworkSummary struct {
	Name string `json:"name"`
	CIDR string `json:"cidr"`
}

// Summarize builds the safe summary of one entry. The config half is nil when
// the bloc has no config, and the state half is nil when it has no state or
// its state is broken. It fails only when a state file the inventory found can
// no longer be read.
func Summarize(entry Entry) (Summary, error) {
	var summary Summary

	if entry.HasConfig {
		summary.Config = summarizeConfig(entry)
	}

	if entry.HasState && !entry.Broken {
		st, err := loadState(entry.Name, entry.StatePath)
		if err != nil {
			return Summary{}, fmt.Errorf("summarizing %s: %w", filepath.Base(entry.StatePath), err)
		}

		summary.State = summarizeState(entry.Name, st)
	}

	return summary, nil
}

// summarizeConfig copies the allowlisted fields and nothing else.
func summarizeConfig(entry Entry) *ConfigSummary {
	summary := &ConfigSummary{File: entry.ConfigFile, Provider: entry.Provider, Region: entry.Region}

	cfg := entry.cfg
	if cfg == nil {
		return summary
	}

	summary.IaaS = cfg.IaaS
	summary.ProjectID = cfg.ProjectID
	summary.OrgID = cfg.OrgID
	summary.NetworkCIDR = networkCIDR(cfg)
	summary.ArtifactsEnabled = cfg.Artifacts.Enabled

	return summary
}

// networkCIDR returns the network CIDR a config sets, under either spelling.
func networkCIDR(cfg *config.Config) string {
	if cfg.Network.CIDR != "" {
		return cfg.Network.CIDR
	}

	return cfg.Network.NetworkCIDR
}

// summarizeState reduces a loaded state to the summary fields.
func summarizeState(name string, st *state.State) *StateSummary {
	summary := &StateSummary{
		Provider:             st.Provider,
		Region:               st.Region,
		CreatedAt:            st.CreatedAt,
		UpdatedAt:            st.UpdatedAt,
		Bastion:              bastionSummary(name, st),
		Network:              networkSummary(st),
		Resources:            make(map[string]int),
		OutputCount:          len(st.Outputs),
		ReservedOutputCount:  0,
		ReservedOutputSample: []string{},
	}

	for key, resource := range st.Resources {
		kind := key
		if resource != nil && resource.Type != "" {
			kind = resource.Type
		} else if dot := strings.IndexByte(key, '.'); dot > 0 {
			kind = key[:dot]
		}

		summary.Resources[kind]++
	}

	reserved := make([]string, 0)

	for key := range st.Outputs {
		if strings.HasPrefix(key, reservedOutputPrefix) {
			reserved = append(reserved, key)
		}
	}

	sort.Strings(reserved)

	summary.ReservedOutputCount = len(reserved)

	for _, key := range reserved {
		if len(summary.ReservedOutputSample) == reservedOutputSampleSize {
			break
		}

		if looksSensitive(key) {
			continue
		}

		summary.ReservedOutputSample = append(summary.ReservedOutputSample, key)
	}

	return summary
}

// looksSensitive reports whether an output name suggests a credential.
func looksSensitive(name string) bool {
	lower := strings.ToLower(name)

	for _, part := range sensitiveNameParts {
		if strings.Contains(lower, part) {
			return true
		}
	}

	return false
}

// bastionSummary reads the bastion facts the bootstrap records as outputs. The
// id falls back to the bastion's instance record for state written without the
// bastion_id output.
func bastionSummary(name string, st *state.State) BastionSummary {
	summary := BastionSummary{
		ID:        stringOutput(st, "bastion_id"),
		PrivateIP: stringOutput(st, "bastion_private_ip"),
		PublicIP:  stringOutput(st, "bastion_public_ip"),
		SSHHost:   stringOutput(st, "bastion_ssh_host"),
	}

	if summary.ID == "" {
		instance := st.Resources[state.ResourceTypeInstance+"."+name+"-bastion"]
		if instance != nil {
			summary.ID = instance.ID
		}
	}

	return summary
}

// stringOutput returns a state output when it is a string, else "".
func stringOutput(st *state.State, key string) string {
	value, ok := st.Outputs[key].(string)
	if !ok {
		return ""
	}

	return value
}

// networkSummary describes the bloc's network. State can hold several, so the
// one whose key sorts first is used for a stable answer.
func networkSummary(st *state.State) NetworkSummary {
	prefix := state.ResourceTypeNetwork + "."
	keys := make([]string, 0)

	for key := range st.Resources {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}

	if len(keys) == 0 {
		return NetworkSummary{}
	}

	sort.Strings(keys)

	network := st.Resources[keys[0]]
	if network == nil {
		return NetworkSummary{}
	}

	cidr, _ := network.Properties["cidr"].(string)

	return NetworkSummary{Name: network.Name, CIDR: cidr}
}
