package cpi

import (
	"context"
	"encoding/json"
)

// BootIdentity captures immutable identities and native attachment metadata.
type BootIdentity struct {
	RootFingerprint        string          `json:"root_fingerprint"`
	ReplacementFingerprint string          `json:"replacement_fingerprint"`
	InstanceFingerprint    string          `json:"instance_fingerprint"`
	InstanceID             string          `json:"instance_id"`
	RootVolumeID           string          `json:"root_volume_id"`
	RootDevice             string          `json:"root_device"`
	DataVolumeIDs          []string        `json:"data_volume_ids"`
	PrivateIPs             []string        `json:"private_ips"`
	ReplacementUserData    string          `json:"replacement_user_data"`
	Metadata               json.RawMessage `json:"metadata"`
}

// BootRefreshRequest identifies an idempotent root-volume creation.
type BootRefreshRequest struct {
	Identity BootIdentity
	Image    string
	Token    string
	Tags     map[string]string
}

// BootVolume preserves the created handle even when waiting for readiness fails.
type BootVolume struct {
	ID          string
	Fingerprint string
}

// BootRefreshManager optionally extends ComputeManager with native boot replacement.
// All mutations must be replayable; SwitchBoot accepts only from, to, or an absent
// root and must verify unchanged data/network identities before changing attachments.
// Identity and metadata may contain user data; persist owner-only and never log them.
type BootRefreshManager interface {
	InspectBoot(ctx context.Context, instanceID string) (*BootIdentity, error)
	PrepareBoot(ctx context.Context, req *BootRefreshRequest) (*BootVolume, error)
	// RecoverBoot finds an existing replacement by transaction identity, never creating it.
	RecoverBoot(ctx context.Context, req *BootRefreshRequest) (*BootVolume, error)
	// SnapshotBoot requires a stopped instance and returns the durable native snapshot.
	SnapshotBoot(ctx context.Context, identity *BootIdentity, token string) (string, error)
	SwitchBoot(ctx context.Context, identity *BootIdentity, fromVolumeID, toVolumeID string) error
	DeleteBoot(ctx context.Context, volumeID, fingerprint string) error
}
