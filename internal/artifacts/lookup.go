package artifacts

import (
	"context"
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// LookupResult bundles the artifacts VM identity for CLI commands and other
// callers that need to operate on the existing VM.
type LookupResult struct {
	VMID         string
	Name         string
	PrivateIP    string
	Endpoint     string
	AccessKey    string
	SecretKey    string
	TLSMode      string
	ZFSDataset   string
	DataVolumeID string
	CACert       string
}

// Lookup resolves the artifacts VM by checking state first, then falling back
// to a provider tag query. Returns nil + nil error when no artifacts VM exists.
func Lookup(ctx context.Context, sm *state.Manager, provider cpi.Provider, blocName string) (*LookupResult, error) {
	vmName := fmt.Sprintf("%s-artifacts", blocName)

	if r, err := sm.GetResource(resourceType, vmName); err == nil && r != nil {
		return resultFromResource(r), nil
	}

	if provider == nil {
		return nil, nil
	}

	insts, err := provider.ComputeManager().ListInstances(ctx, map[string]string{
		"ocfp:role": "artifacts",
		"ocfp:bloc": blocName,
	})
	if err != nil {
		return nil, fmt.Errorf("listing artifacts instances by tag: %w", err)
	}

	for _, inst := range insts {
		if inst.Name == vmName {
			return &LookupResult{
				VMID:      inst.ID,
				Name:      inst.Name,
				PrivateIP: inst.PrivateIP,
			}, nil
		}
	}

	return nil, nil
}

func resultFromResource(r *state.Resource) *LookupResult {
	get := func(k string) string {
		v, ok := r.Properties[k].(string)
		if !ok {
			return ""
		}
		return v
	}

	return &LookupResult{
		VMID:         get("vm_id"),
		Name:         r.Name,
		PrivateIP:    get("private_ip"),
		Endpoint:     get("endpoint"),
		AccessKey:    get("access_key"),
		SecretKey:    get("secret_key"),
		TLSMode:      get("tls_mode"),
		ZFSDataset:   get("zfs_dataset"),
		DataVolumeID: get("data_volume_id"),
		CACert:       get("ca_cert"),
	}
}
