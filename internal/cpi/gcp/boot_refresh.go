package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"cloud.google.com/go/compute/apiv1/computepb"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"google.golang.org/protobuf/encoding/protojson"
)

// Native boot replacement is supported only while stopped. Never force a disk
// detach. https://docs.cloud.google.com/compute/docs/disks/detach-reattach-boot-disk
var _ cpi.BootRefreshManager = (*ComputeManager)(nil)

type gcpBootMetadata struct {
	DataFingerprints map[string]string                `json:"data_fingerprints"`
	InstanceNumber   uint64                           `json:"instance_number"`
	Attachment       json.RawMessage                  `json:"attachment"`
	Startup          map[string]string                `json:"startup"`
	Networks         []string                         `json:"networks"`
	DiskType         string                           `json:"disk_type"`
	Size             int64                            `json:"size"`
	Architecture     string                           `json:"architecture"`
	Encryption       *computepb.CustomerEncryptionKey `json:"encryption,omitempty"`
}

func (m *ComputeManager) bootInstance(ctx context.Context, id string) (*computepb.Instance, error) {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return nil, err
	}

	canonical, err := m.canonicalInstanceID(ctx, id)
	if err != nil {
		return nil, err
	}

	cfg := m.client.getConfig()
	project, zone, name := resourceScope(cfg.ProjectID, cfg.Zone, canonical)
	vm, err := m.client.getInstancesClient().Get(ctx, &computepb.GetInstanceRequest{Project: project, Zone: zone, Instance: name})

	return vm, WrapGCPError(err, "inspect boot instance")
}
func (m *ComputeManager) bootDisk(ctx context.Context, id string) (*computepb.Disk, error) {
	cfg := m.client.getConfig()
	project, zone, name := resourceScope(cfg.ProjectID, cfg.Zone, id)
	disk, err := m.client.getDisksClient().Get(ctx, &computepb.GetDiskRequest{Project: project, Zone: zone, Disk: name})

	return disk, WrapGCPError(err, "inspect boot disk")
}
func bootParts(vm *computepb.Instance) (*computepb.AttachedDisk, []string, error) {
	var (
		root *computepb.AttachedDisk
		data []string
	)

	for _, disk := range vm.GetDisks() {
		if disk.GetType() == "SCRATCH" {
			return nil, nil, errors.New("boot refresh cannot preserve local scratch disks")
		}

		if disk.GetBoot() {
			if root != nil {
				return nil, nil, errors.New("multiple boot disks")
			}

			root = disk
		} else {
			data = append(data, canonicalURL(disk.GetSource()))
		}
	}

	sort.Strings(data)

	return root, data, nil
}
func bootNetworks(vm *computepb.Instance) ([]string, []string) {
	networks := make([]string, 0, len(vm.GetNetworkInterfaces()))
	ips := make([]string, 0, len(vm.GetNetworkInterfaces()))

	for _, nic := range vm.GetNetworkInterfaces() {
		networks = append(networks, strings.Join([]string{nic.GetName(), canonicalURL(nic.GetNetwork()), canonicalURL(nic.GetSubnetwork()), nic.GetNetworkIP()}, "|"))
		ips = append(ips, nic.GetNetworkIP())
	}

	sort.Strings(networks)
	sort.Strings(ips)

	return networks, ips
}
func (m *ComputeManager) InspectBoot(ctx context.Context, id string) (*cpi.BootIdentity, error) {
	vm, err := m.bootInstance(ctx, id)
	if err != nil {
		return nil, err
	}

	root, data, err := bootParts(vm)
	if err != nil {
		return nil, err
	}

	if root == nil {
		return nil, errors.New("instance has no boot disk")
	}

	diskID := canonicalURL(root.GetSource())

	disk, err := m.bootDisk(ctx, diskID)
	if err != nil {
		return nil, err
	}

	if key := disk.GetDiskEncryptionKey(); key != nil && (key.GetSha256() != "" && key.GetKmsKeyName() == "") {
		return nil, errors.New("boot refresh requires recoverable KMS encryption; customer-supplied key unavailable")
	}

	attachment, err := protojson.Marshal(root)
	if err != nil {
		return nil, err
	}

	networks, ips := bootNetworks(vm)

	fingerprints, err := m.bootDataFingerprints(ctx, data)
	if err != nil {
		return nil, err
	}

	fingerprintBytes, err := json.Marshal(fingerprints)
	if err != nil {
		return nil, err
	}

	metadata := gcpBootMetadata{DataFingerprints: fingerprints, InstanceNumber: vm.GetId(), Attachment: attachment, Networks: networks, Size: disk.GetSizeGb(), DiskType: disk.GetType(), Architecture: disk.GetArchitecture(), Encryption: disk.GetDiskEncryptionKey(), Startup: map[string]string{}}
	for _, item := range vm.GetMetadata().GetItems() {
		if isBootMetadataKey(item.GetKey()) {
			metadata.Startup[item.GetKey()] = item.GetValue()
		}
	}

	raw, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}

	return &cpi.BootIdentity{InstanceID: resourceIdentity(m.client.getConfig(), vm.GetZone(), "instances", vm.GetName()), InstanceFingerprint: strconv.FormatUint(vm.GetId(), 10) + "|" + strings.Join(networks, ";") + "|" + string(fingerprintBytes), RootFingerprint: strconv.FormatUint(disk.GetId(), 10), RootVolumeID: diskID, RootDevice: root.GetDeviceName(), DataVolumeIDs: data, PrivateIPs: ips, Metadata: raw}, nil
}
func isBootMetadataKey(key string) bool {
	return key == "user-data" || key == "startup-script" || key == "startup-script-url"
}
func parseBootMetadata(identity *cpi.BootIdentity) (*gcpBootMetadata, error) {
	var metadata gcpBootMetadata

	if identity == nil || identity.InstanceID == "" || identity.RootVolumeID == "" || identity.RootDevice == "" {
		return nil, errors.New("incomplete boot identity")
	}

	if err := json.Unmarshal(identity.Metadata, &metadata); err != nil {
		return nil, fmt.Errorf("invalid boot metadata: %w", err)
	}

	if len(metadata.Attachment) == 0 || metadata.Size <= 0 {
		return nil, errors.New("incomplete native boot metadata")
	}

	return &metadata, nil
}
func bootTokenName(token, role string) string {
	sum := sha256.Sum256([]byte(token))

	return "ocfp-" + role + "-" + hex.EncodeToString(sum[:12])
}
func bootRequestID(token string) *string {
	sum := sha256.Sum256([]byte(token))
	sum[6] = (sum[6] & 15) | 64
	sum[8] = (sum[8] & 63) | 128
	value := fmt.Sprintf("%x-%x-%x-%x-%x", sum[:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])

	return &value
}
func (m *ComputeManager) PrepareBoot(ctx context.Context, req *cpi.BootRefreshRequest) (*cpi.BootVolume, error) {
	if req == nil || req.Image == "" || req.Token == "" {
		return nil, errors.New("boot image and transaction token required")
	}

	metadata, err := parseBootMetadata(&req.Identity)
	if err != nil {
		return nil, err
	}

	if err = m.verifyOriginalBoot(ctx, &req.Identity, false); err != nil {
		return nil, err
	}

	image, err := m.GetImage(ctx, req.Image)
	if err != nil {
		return nil, err
	}

	cfg := m.client.getConfig()
	imageProject, _, imageName := resourceScope(cfg.ProjectID, "", image.ID)

	nativeImage, err := m.client.getImagesClient().Get(ctx, &computepb.GetImageRequest{Project: imageProject, Image: imageName})
	if err != nil {
		return nil, WrapGCPError(err, "inspect boot image")
	}

	if metadata.Architecture != "" && nativeImage.GetArchitecture() != "" && metadata.Architecture != nativeImage.GetArchitecture() {
		return nil, errors.New("replacement image architecture differs from current root")
	}

	project, zone, _ := resourceScope(cfg.ProjectID, cfg.Zone, req.Identity.InstanceID)
	name := bootTokenName(req.Token, "boot")
	id := scopedResource(project, zone, "disks", name)
	handle := &cpi.BootVolume{ID: id}
	size := max(metadata.Size, nativeImage.GetDiskSizeGb())
	labels := BuildLabels(name, req.Tags)
	labels["ocfp-refresh-token"] = req.Token
	labels["ocfp-role"] = "boot-refresh"
	source := FormatImageURL(imageProject, imageName)

	existing, lookupErr := m.bootDisk(ctx, id)
	if lookupErr == nil {
		return checkedBootVolume(existing, id, source, req.Token, size, metadata, true)
	}

	if !IsNotFound(lookupErr) {
		return nil, lookupErr
	}

	disk := &computepb.Disk{Name: proto(name), SourceImage: proto(source), SizeGb: proto(size), Type: proto(metadata.DiskType), Labels: labels}
	if metadata.Encryption != nil && metadata.Encryption.GetKmsKeyName() != "" {
		disk.DiskEncryptionKey = &computepb.CustomerEncryptionKey{KmsKeyName: proto(metadata.Encryption.GetKmsKeyName())}
	}

	op, err := m.client.getDisksClient().Insert(ctx, &computepb.InsertDiskRequest{Project: project, Zone: zone, DiskResource: disk, RequestId: bootRequestID(req.Token)})
	if err != nil {
		// A lost response is recovered by the deterministic name and token on retry.
		// Do not journal an unverified handle after a conflicting insert.
		return nil, WrapGCPError(err, "prepare boot disk")
	}

	return m.finishPreparedBoot(ctx, handle, source, req.Token, size, metadata, WrapGCPError(op.Wait(ctx), "wait for boot disk"))
}

func (m *ComputeManager) SnapshotBoot(ctx context.Context, identity *cpi.BootIdentity, token string) (string, error) {
	if _, err := parseBootMetadata(identity); err != nil {
		return "", err
	}

	if token == "" {
		return "", errors.New("snapshot transaction token required")
	}

	if err := m.verifyOriginalBoot(ctx, identity, true); err != nil {
		return "", err
	}

	cfg := m.client.getConfig()
	project, _, _ := resourceScope(cfg.ProjectID, cfg.Zone, identity.InstanceID)
	name := bootTokenName(token, "recovery")

	snapshot, err := m.client.getSnapshotsClient().Get(ctx, &computepb.GetSnapshotRequest{Project: project, Snapshot: name})
	if err == nil {
		if canonicalURL(snapshot.GetSourceDisk()) != canonicalURL(identity.RootVolumeID) || snapshot.GetLabels()["ocfp-refresh-token"] != token || snapshot.GetSourceDiskId() != identity.RootFingerprint {
			return "", errors.New("snapshot name occupied by unrelated recovery data")
		}

		if snapshot.GetStatus() != "READY" {
			return name, errors.New("recovery snapshot is not ready")
		}

		return name, nil
	}

	if !IsNotFound(WrapGCPError(err, "snapshot lookup")) {
		return "", err
	}

	originalDisk, err := m.bootDisk(ctx, identity.RootVolumeID)
	if err != nil {
		return "", err
	}

	if identity.RootFingerprint == "" || identity.RootFingerprint == "0" || strconv.FormatUint(originalDisk.GetId(), 10) != identity.RootFingerprint {
		return "", errors.New("original root immutable identity changed")
	}

	op, err := m.client.getSnapshotsClient().Insert(ctx, &computepb.InsertSnapshotRequest{Project: project, RequestId: bootRequestID(token + "snapshot"), SnapshotResource: &computepb.Snapshot{Name: proto(name), SourceDisk: proto(identity.RootVolumeID), Labels: map[string]string{"ocfp-refresh-token": token, "ocfp-role": "boot-recovery"}}})
	if err != nil {
		return name, WrapGCPError(err, "snapshot original root")
	}

	return name, WrapGCPError(op.Wait(ctx), "wait recovery snapshot")
}
func (m *ComputeManager) checkBootIdentity(ctx context.Context, vm *computepb.Instance, identity *cpi.BootIdentity, metadata *gcpBootMetadata) (*computepb.AttachedDisk, error) {
	if vm.GetStatus() != "TERMINATED" {
		return nil, errors.New("boot disk changes require a stopped instance")
	}

	if vm.GetId() != metadata.InstanceNumber {
		return nil, errors.New("instance was replaced since boot transaction began")
	}

	root, data, err := bootParts(vm)
	if err != nil {
		return nil, err
	}

	networks, ips := bootNetworks(vm)
	if !reflect.DeepEqual(data, identity.DataVolumeIDs) || !reflect.DeepEqual(networks, metadata.Networks) || !reflect.DeepEqual(ips, identity.PrivateIPs) {
		return nil, errors.New("data or network identities changed during boot refresh")
	}

	actual, err := m.bootDataFingerprints(ctx, data)
	if err != nil {
		return nil, err
	}

	if !reflect.DeepEqual(actual, metadata.DataFingerprints) {
		return nil, errors.New("preserved data disk immutable identity changed")
	}

	return root, nil
}
func (m *ComputeManager) SwitchBoot(ctx context.Context, identity *cpi.BootIdentity, from, to string) error {
	metadata, err := parseBootMetadata(identity)
	if err != nil {
		return err
	}

	if err := validateBootPair(identity, from, to); err != nil {
		return err
	}

	vm, err := m.bootInstance(ctx, identity.InstanceID)
	if err != nil {
		return err
	}

	root, err := m.checkBootIdentity(ctx, vm, identity, metadata)
	if err != nil {
		return err
	}

	if root != nil && canonicalURL(root.GetSource()) != canonicalURL(from) && canonicalURL(root.GetSource()) != canonicalURL(to) {
		return errors.New("unexpected root disk; refusing replacement")
	}

	cfg := m.client.getConfig()
	project, zone, name := resourceScope(cfg.ProjectID, cfg.Zone, identity.InstanceID)

	if err := m.validateBootAttachments(ctx, identity, root, to, project, zone); err != nil {
		return err
	}

	if root != nil && canonicalURL(root.GetSource()) == canonicalURL(from) {
		op, err := m.client.getInstancesClient().DetachDisk(ctx, &computepb.DetachDiskInstanceRequest{Project: project, Zone: zone, Instance: name, DeviceName: root.GetDeviceName()})
		if err != nil {
			return WrapGCPError(err, "detach original root")
		}

		if err = op.Wait(ctx); err != nil {
			return err
		}

		root = nil
	}

	if root == nil {
		var attachment computepb.AttachedDisk
		if err := protojson.Unmarshal(metadata.Attachment, &attachment); err != nil {
			return err
		}

		attachment.Source = proto(to)
		attachment.Boot = proto(true)
		attachment.InitializeParams = nil
		attachment.Index = nil

		op, err := m.client.getInstancesClient().AttachDisk(ctx, &computepb.AttachDiskInstanceRequest{Project: project, Zone: zone, Instance: name, AttachedDiskResource: &attachment})
		if err != nil {
			return WrapGCPError(err, "attach replacement root")
		}

		if err = op.Wait(ctx); err != nil {
			return err
		}
	}

	return m.restoreBootMetadata(ctx, identity, metadata, to)
}
func (m *ComputeManager) restoreBootMetadata(ctx context.Context, identity *cpi.BootIdentity, original *gcpBootMetadata, target string) error {
	vm, err := m.bootInstance(ctx, identity.InstanceID)
	if err != nil {
		return err
	}

	root, err := m.checkBootIdentity(ctx, vm, identity, original)
	if err != nil {
		return err
	}

	if root == nil || canonicalURL(root.GetSource()) != canonicalURL(target) {
		return errors.New("target boot attachment was not observed")
	}

	restore := canonicalURL(target) == canonicalURL(identity.RootVolumeID)

	current := vm.GetMetadata()
	if current == nil {
		current = &computepb.Metadata{}
	}

	items := []*computepb.Items{}

	for _, item := range current.GetItems() {
		if !isBootMetadataKey(item.GetKey()) {
			items = append(items, item)
		}
	}

	values := original.Startup

	if !restore {
		if identity.ReplacementUserData == "" {
			return errors.New("replacement guest startup data is missing")
		}

		key := "startup-script"
		if strings.HasPrefix(strings.TrimSpace(identity.ReplacementUserData), "#cloud-config") {
			key = "user-data"
		}

		values = map[string]string{key: identity.ReplacementUserData}
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	for _, key := range keys {
		items = append(items, &computepb.Items{Key: proto(key), Value: proto(values[key])})
	}

	cfg := m.client.getConfig()
	project, zone, name := resourceScope(cfg.ProjectID, cfg.Zone, identity.InstanceID)

	op, err := m.client.getInstancesClient().SetMetadata(ctx, &computepb.SetMetadataInstanceRequest{Project: project, Zone: zone, Instance: name, MetadataResource: &computepb.Metadata{Fingerprint: current.Fingerprint, Items: items}})
	if err != nil {
		return WrapGCPError(err, "update boot metadata")
	}

	return op.Wait(ctx)
}
func (m *ComputeManager) DeleteBoot(ctx context.Context, id, fingerprint string) error {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return err
	}

	disk, err := m.bootDisk(ctx, id)
	if IsNotFound(err) {
		return nil
	}

	if err != nil {
		return err
	}

	if fingerprint == "" || fingerprint == "0" || strconv.FormatUint(disk.GetId(), 10) != fingerprint {
		return errors.New("boot disk immutable identity changed")
	}

	if len(disk.GetUsers()) != 0 {
		return errors.New("refusing to delete attached boot disk")
	}

	return (&StorageManager{client: m.client}).DeleteVolume(ctx, id)
}

func validatePreparedBoot(existing *computepb.Disk, source, token string, size int64) error {
	if canonicalURL(existing.GetSourceImage()) != canonicalURL(source) || existing.GetLabels()["ocfp-refresh-token"] != token || existing.GetSizeGb() < size {
		return errors.New("replacement boot name occupied by unrelated disk")
	}

	return nil
}

func validateBootPair(identity *cpi.BootIdentity, from, to string) error {
	if from == "" || to == "" || from == to {
		return errors.New("distinct source and target boot disks required")
	}

	if canonicalURL(from) != canonicalURL(identity.RootVolumeID) && canonicalURL(to) != canonicalURL(identity.RootVolumeID) {
		return errors.New("boot switch must include the recorded original root")
	}

	if canonicalURL(to) != canonicalURL(identity.RootVolumeID) && identity.ReplacementUserData == "" {
		return errors.New("replacement guest startup data is missing")
	}

	return nil
}

func (m *ComputeManager) validateBootTarget(ctx context.Context, identity *cpi.BootIdentity, to, project, zone string) error {
	targetProject, targetZone, _ := resourceScope(project, zone, to)
	if targetProject != project || targetZone != zone {
		return errors.New("boot replacement must remain in original project and zone")
	}

	target, err := m.bootDisk(ctx, to)
	if err != nil {
		return err
	}

	expected := identity.ReplacementFingerprint
	if canonicalURL(to) == canonicalURL(identity.RootVolumeID) {
		expected = identity.RootFingerprint
	}

	if expected == "" || expected == "0" || strconv.FormatUint(target.GetId(), 10) != expected {
		return errors.New("boot target immutable identity changed")
	}

	for _, user := range target.GetUsers() {
		if canonicalURL(user) != canonicalURL(identity.InstanceID) {
			return errors.New("target boot disk belongs to another instance")
		}
	}

	return nil
}

func (m *ComputeManager) bootDataFingerprints(ctx context.Context, ids []string) (map[string]string, error) {
	fingerprints := make(map[string]string, len(ids))
	for _, id := range ids {
		disk, err := m.bootDisk(ctx, id)
		if err != nil {
			return nil, err
		}

		if disk.GetId() == 0 {
			return nil, errors.New("data disk immutable identity unavailable")
		}

		fingerprints[id] = strconv.FormatUint(disk.GetId(), 10)
	}

	return fingerprints, nil
}

// RecoverBoot looks up a transaction without allocating resources or resolving a new image.
func (m *ComputeManager) RecoverBoot(ctx context.Context, req *cpi.BootRefreshRequest) (*cpi.BootVolume, error) {
	if req == nil || req.Token == "" {
		return nil, errors.New("boot transaction token required")
	}

	metadata, err := parseBootMetadata(&req.Identity)
	if err != nil {
		return nil, err
	}

	if err = m.client.ensureClientsLoaded(ctx); err != nil {
		return nil, err
	}

	cfg := m.client.getConfig()
	project, zone, _ := resourceScope(cfg.ProjectID, cfg.Zone, req.Identity.InstanceID)
	id := scopedResource(project, zone, "disks", bootTokenName(req.Token, "boot"))

	disk, err := m.bootDisk(ctx, id)
	if IsNotFound(err) {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	if err = validatePreparedBoot(disk, req.Image, req.Token, metadata.Size); err != nil {
		return nil, err
	}

	if err = validateBootDiskProperties(disk, metadata); err != nil {
		return nil, err
	}

	return &cpi.BootVolume{ID: id, Fingerprint: strconv.FormatUint(disk.GetId(), 10)}, nil
}
func validateBootDiskProperties(disk *computepb.Disk, metadata *gcpBootMetadata) error {
	if disk.GetId() == 0 {
		return errors.New("boot disk immutable identity unavailable")
	}

	if canonicalURL(disk.GetType()) != canonicalURL(metadata.DiskType) {
		return errors.New("replacement boot disk type changed")
	}

	if disk.GetDiskEncryptionKey().GetKmsKeyName() != metadata.Encryption.GetKmsKeyName() {
		return errors.New("replacement boot disk encryption changed")
	}

	return nil
}

func (m *ComputeManager) finishPreparedBoot(ctx context.Context, handle *cpi.BootVolume, source, token string, size int64, metadata *gcpBootMetadata, waitErr error) (*cpi.BootVolume, error) {
	created, lookupErr := m.bootDisk(ctx, handle.ID)
	if lookupErr != nil {
		return handle, errors.Join(waitErr, lookupErr)
	}

	if err := validatePreparedBoot(created, source, token, size); err != nil {
		return nil, errors.Join(waitErr, err)
	}

	if err := validateBootDiskProperties(created, metadata); err != nil {
		return nil, err
	}

	handle.Fingerprint = strconv.FormatUint(created.GetId(), 10)

	return handle, waitErr
}
func (m *ComputeManager) validateBootAttachments(ctx context.Context, identity *cpi.BootIdentity, root *computepb.AttachedDisk, to, project, zone string) error {
	if err := m.validateBootTarget(ctx, identity, to, project, zone); err != nil {
		return err
	}

	if root != nil {
		if err := m.validateBootTarget(ctx, identity, canonicalURL(root.GetSource()), project, zone); err != nil {
			return err
		}
	}

	return nil
}

func (m *ComputeManager) verifyOriginalBoot(ctx context.Context, identity *cpi.BootIdentity, stopped bool) error {
	actual, err := m.InspectBoot(ctx, identity.InstanceID)
	if err != nil {
		return err
	}

	if actual.InstanceFingerprint != identity.InstanceFingerprint || actual.RootVolumeID != identity.RootVolumeID || actual.RootFingerprint != identity.RootFingerprint {
		return errors.New("original boot identity changed")
	}

	if stopped {
		vm, err := m.bootInstance(ctx, identity.InstanceID)
		if err != nil {
			return err
		}

		if vm.GetStatus() != "TERMINATED" {
			return errors.New("recovery snapshot requires a stopped instance")
		}
	}

	return nil
}

func checkedBootVolume(disk *computepb.Disk, id, source, token string, size int64, metadata *gcpBootMetadata, ready bool) (*cpi.BootVolume, error) {
	if err := validatePreparedBoot(disk, source, token, size); err != nil {
		return nil, err
	}

	if err := validateBootDiskProperties(disk, metadata); err != nil {
		return nil, err
	}

	handle := &cpi.BootVolume{ID: id, Fingerprint: strconv.FormatUint(disk.GetId(), 10)}
	if ready && disk.GetStatus() != "READY" {
		return handle, errors.New("replacement boot disk is not ready")
	}

	return handle, nil
}
