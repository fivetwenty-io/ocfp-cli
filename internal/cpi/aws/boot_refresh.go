package aws

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

type bootAttributeReader interface {
	DescribeInstanceAttribute(ctx context.Context, input *ec2.DescribeInstanceAttributeInput, opts ...func(*ec2.Options)) (*ec2.DescribeInstanceAttributeOutput, error)
}
type bootMetadata struct {
	Zone                string                   `json:"zone"`
	Interfaces          []string                 `json:"interfaces"`
	Architecture        types.ArchitectureValues `json:"architecture"`
	Virtualization      types.VirtualizationType `json:"virtualization"`
	BootMode            types.BootModeValues     `json:"boot_mode"`
	DeleteOnTermination bool                     `json:"delete_on_termination"`
	UserData            string                   `json:"user_data"`
	Size                int32                    `json:"size"`
	Encrypted           bool                     `json:"encrypted"`
	KMSKeyID            string                   `json:"k_m_s_key_i_d"`
}

var errBootIdentity = errors.New("AWS boot refresh identity or attachment mismatch")

func bootInstance(ctx context.Context, client EC2API, id string) (*types.Instance, error) {
	out, err := client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{id}})
	if err != nil {
		return nil, err
	}

	var found *types.Instance

	for _, r := range out.Reservations {
		for _, i := range r.Instances {
			if found != nil || aws.ToString(i.InstanceId) != id {
				return nil, errBootIdentity
			}

			instance := i
			found = &instance
		}
	}

	if found == nil {
		return nil, errBootIdentity
	}

	return found, nil
}
func bootVolume(ctx context.Context, client EC2API, id string) (*types.Volume, error) {
	out, err := client.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{id}})
	if err != nil {
		return nil, err
	}

	if len(out.Volumes) != 1 || aws.ToString(out.Volumes[0].VolumeId) != id {
		return nil, errBootIdentity
	}

	return &out.Volumes[0], nil
}
func bootObserved(i *types.Instance) (string, []string, []string, []string) {
	root := ""

	var data, ips []string

	interfaces := make([]string, 0, len(i.NetworkInterfaces))
	for _, b := range i.BlockDeviceMappings {
		if b.Ebs == nil {
			continue
		}

		if aws.ToString(b.DeviceName) == aws.ToString(i.RootDeviceName) {
			root = aws.ToString(b.Ebs.VolumeId)
		} else {
			data = append(data, aws.ToString(b.Ebs.VolumeId))
		}
	}

	for _, n := range i.NetworkInterfaces {
		interfaces = append(interfaces, aws.ToString(n.NetworkInterfaceId))
		if len(n.PrivateIpAddresses) == 0 {
			ips = append(ips, aws.ToString(n.PrivateIpAddress))
		}

		for _, ip := range n.PrivateIpAddresses {
			ips = append(ips, aws.ToString(ip.PrivateIpAddress))
		}
	}

	slices.Sort(data)
	slices.Sort(ips)
	slices.Sort(interfaces)

	return root, data, ips, interfaces
}
func (m *ComputeManager) InspectBoot(ctx context.Context, instanceID string) (*cpi.BootIdentity, error) {
	client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	i, err := bootInstance(ctx, client, instanceID)
	if err != nil {
		return nil, err
	}

	root, data, ips, interfaces := bootObserved(i)
	if root == "" || i.RootDeviceType != types.DeviceTypeEbs || i.Placement == nil {
		return nil, errBootIdentity
	}

	v, err := bootVolume(ctx, client, root)
	if err != nil {
		return nil, err
	}

	reader, ok := client.(bootAttributeReader)
	if !ok {
		return nil, ErrUnsupportedFeature
	}

	attr, err := reader.DescribeInstanceAttribute(ctx, &ec2.DescribeInstanceAttributeInput{InstanceId: aws.String(instanceID), Attribute: types.InstanceAttributeNameUserData})
	if err != nil {
		return nil, err
	}

	md := bootMetadata{Zone: aws.ToString(i.Placement.AvailabilityZone), Interfaces: interfaces, Architecture: i.Architecture, Virtualization: i.VirtualizationType, BootMode: i.BootMode, Size: aws.ToInt32(v.Size), Encrypted: aws.ToBool(v.Encrypted), KMSKeyID: aws.ToString(v.KmsKeyId)}
	if attr.UserData != nil {
		decoded, decodeErr := base64.StdEncoding.DecodeString(aws.ToString(attr.UserData.Value))
		if decodeErr != nil {
			return nil, errors.New("invalid original user data encoding")
		}

		md.UserData = string(decoded)
	}

	for _, b := range i.BlockDeviceMappings {
		if b.Ebs != nil && aws.ToString(b.Ebs.VolumeId) == root {
			md.DeleteOnTermination = aws.ToBool(b.Ebs.DeleteOnTermination)
		}
	}

	raw, err := json.Marshal(md)
	if err != nil {
		return nil, err
	}

	return &cpi.BootIdentity{InstanceID: instanceID, InstanceFingerprint: instanceID + "|" + strings.Join(interfaces, ","), RootVolumeID: root, RootFingerprint: root, RootDevice: aws.ToString(i.RootDeviceName), DataVolumeIDs: data, PrivateIPs: ips, Metadata: raw}, nil
}
func decodeBoot(identity *cpi.BootIdentity) (bootMetadata, error) {
	var md bootMetadata
	if identity == nil || identity.InstanceID == "" || identity.RootVolumeID == "" || identity.RootDevice == "" {
		return md, errBootIdentity
	}

	if err := json.Unmarshal(identity.Metadata, &md); err != nil {
		return md, err
	}

	if md.Zone == "" || len(md.Interfaces) == 0 {
		return md, errBootIdentity
	}

	return md, nil
}
func validateBootObserved(i *types.Instance, identity *cpi.BootIdentity, md bootMetadata) error {
	_, data, ips, interfaces := bootObserved(i)
	if i.Placement == nil || aws.ToString(i.Placement.AvailabilityZone) != md.Zone || aws.ToString(i.RootDeviceName) != identity.RootDevice || !slices.Equal(data, identity.DataVolumeIDs) || !slices.Equal(ips, identity.PrivateIPs) || !slices.Equal(interfaces, md.Interfaces) {
		return errBootIdentity
	}

	return nil
}
func (m *ComputeManager) PrepareBoot(ctx context.Context, req *cpi.BootRefreshRequest) (*cpi.BootVolume, error) {
	if req == nil || req.Token == "" || req.Image == "" {
		return nil, errBootIdentity
	}

	md, err := decodeBoot(&req.Identity)
	if err != nil {
		return nil, err
	}

	client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	i, err := bootInstance(ctx, client, req.Identity.InstanceID)
	if err != nil {
		return nil, err
	}

	if err = validateBootObserved(i, &req.Identity, md); err != nil {
		return nil, err
	}

	if bootRoot(i) != req.Identity.RootVolumeID {
		return nil, errBootIdentity
	}

	snapshot, size, err := bootImage(ctx, client, req.Image, md)
	if err != nil {
		return nil, err
	}

	tags := map[string]string{}
	for k, v := range req.Tags {
		tags[k] = v
	}

	tags["ocfp:boot-refresh"] = req.Token
	tags["ocfp:instance"] = req.Identity.InstanceID

	input := &ec2.CreateVolumeInput{AvailabilityZone: aws.String(md.Zone), SnapshotId: aws.String(snapshot), Size: aws.Int32(size), VolumeType: types.VolumeTypeGp3, ClientToken: aws.String(req.Token), TagSpecifications: []types.TagSpecification{{ResourceType: types.ResourceTypeVolume, Tags: buildResourceTags(tags)}}}
	if md.Encrypted {
		input.Encrypted = aws.Bool(true)
		if md.KMSKeyID != "" {
			input.KmsKeyId = aws.String(md.KMSKeyID)
		}
	}

	created, err := client.CreateVolume(ctx, input)
	if err != nil {
		return nil, err
	}

	id := aws.ToString(created.VolumeId)
	if id == "" {
		return nil, errBootIdentity
	}

	return &cpi.BootVolume{ID: id, Fingerprint: id}, ec2.NewVolumeAvailableWaiter(client).Wait(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{id}}, 10*time.Minute)
}

func (m *ComputeManager) SnapshotBoot(ctx context.Context, identity *cpi.BootIdentity, token string) (string, error) {
	if token == "" {
		return "", errBootIdentity
	}

	md, err := decodeBoot(identity)
	if err != nil {
		return "", err
	}

	client, err := m.getEC2(ctx)
	if err != nil {
		return "", err
	}

	i, err := bootInstance(ctx, client, identity.InstanceID)
	if err != nil {
		return "", err
	}

	if err = validateBootObserved(i, identity, md); err != nil {
		return "", err
	}

	root := bootRoot(i)
	if root != identity.RootVolumeID || i.State == nil || i.State.Name != types.InstanceStateNameStopped {
		return "", errBootIdentity
	}

	var (
		found string
		next  *string
	)
	for {
		out, listErr := client.DescribeSnapshots(ctx, &ec2.DescribeSnapshotsInput{OwnerIds: []string{"self"}, Filters: []types.Filter{{Name: aws.String("tag:ocfp:boot-refresh"), Values: []string{token}}, {Name: aws.String("volume-id"), Values: []string{identity.RootVolumeID}}}, NextToken: next})
		if listErr != nil {
			return "", listErr
		}

		for _, s := range out.Snapshots {
			if found != "" || aws.ToString(s.VolumeId) != identity.RootVolumeID {
				return "", errBootIdentity
			}

			found = aws.ToString(s.SnapshotId)
		}

		next = out.NextToken
		if aws.ToString(next) == "" {
			break
		}
	}

	if found == "" {
		out, createErr := client.CreateSnapshot(ctx, &ec2.CreateSnapshotInput{VolumeId: aws.String(identity.RootVolumeID), TagSpecifications: []types.TagSpecification{{ResourceType: types.ResourceTypeSnapshot, Tags: buildResourceTags(map[string]string{"ocfp:boot-refresh": token, "ocfp:instance": identity.InstanceID})}}})
		if createErr != nil {
			return "", createErr
		}

		found = aws.ToString(out.SnapshotId)
	}

	if found == "" {
		return "", errBootIdentity
	}

	return found, ec2.NewSnapshotCompletedWaiter(client).Wait(ctx, &ec2.DescribeSnapshotsInput{SnapshotIds: []string{found}}, 30*time.Minute)
}

func (m *ComputeManager) SwitchBoot(ctx context.Context, identity *cpi.BootIdentity, fromVolumeID, toVolumeID string) error {
	md, err := decodeBoot(identity)
	if err != nil {
		return err
	}

	if fromVolumeID == "" || toVolumeID == "" || fromVolumeID == toVolumeID || (fromVolumeID != identity.RootVolumeID && toVolumeID != identity.RootVolumeID) {
		return errBootIdentity
	}

	client, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	modifier, ok := client.(instanceAttributeAPI)
	if !ok {
		return ErrUnsupportedFeature
	}

	i, err := bootInstance(ctx, client, identity.InstanceID)
	if err != nil {
		return err
	}

	if err = validateBootObserved(i, identity, md); err != nil {
		return err
	}

	root := bootRoot(i)

	userdata, err := validateBootSwitch(ctx, client, i, identity, md, fromVolumeID, toVolumeID)
	if err != nil {
		return err
	}

	if err = applyBootSwitch(ctx, client, modifier, identity, md, root, fromVolumeID, toVolumeID, userdata); err != nil {
		return err
	}

	after, err := bootInstance(ctx, client, identity.InstanceID)
	if err != nil {
		return err
	}

	if err = validateBootObserved(after, identity, md); err != nil {
		return err
	}

	actual := bootRoot(after)
	if actual != toVolumeID {
		return errBootIdentity
	}

	return nil
}
func (m *ComputeManager) DeleteBoot(ctx context.Context, volumeID, fingerprint string) error {
	if volumeID == "" || volumeID != fingerprint {
		return errBootIdentity
	}

	client, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	v, err := bootVolume(ctx, client, volumeID)
	if err != nil {
		if IsNotFound(err) {
			return nil
		}

		return err
	}

	if len(v.Attachments) > 0 {
		return errBootIdentity
	}

	_, err = client.DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: aws.String(volumeID)})

	return err
}

var _ cpi.BootRefreshManager = (*ComputeManager)(nil)

func bootRoot(i *types.Instance) string {
	for _, b := range i.BlockDeviceMappings {
		if b.Ebs != nil && aws.ToString(b.DeviceName) == aws.ToString(i.RootDeviceName) {
			return aws.ToString(b.Ebs.VolumeId)
		}
	}

	return ""
}

func bootImage(ctx context.Context, client EC2API, imageID string, md bootMetadata) (string, int32, error) {
	out, err := client.DescribeImages(ctx, &ec2.DescribeImagesInput{ImageIds: []string{imageID}})
	if err != nil {
		return "", 0, err
	}

	if len(out.Images) != 1 {
		return "", 0, errBootIdentity
	}

	image := out.Images[0]
	if image.RootDeviceType != types.DeviceTypeEbs || image.Architecture != md.Architecture || image.VirtualizationType != md.Virtualization || len(image.ProductCodes) > 0 || (image.BootMode != "" && md.BootMode != "" && image.BootMode != md.BootMode) {
		return "", 0, errors.New("replacement AMI incompatible with existing instance")
	}

	snapshot := ""
	size := md.Size

	for _, b := range image.BlockDeviceMappings {
		if aws.ToString(b.DeviceName) == aws.ToString(image.RootDeviceName) && b.Ebs != nil {
			snapshot = aws.ToString(b.Ebs.SnapshotId)
			if aws.ToInt32(b.Ebs.VolumeSize) > size {
				size = aws.ToInt32(b.Ebs.VolumeSize)
			}
		}
	}

	if snapshot == "" {
		return "", 0, errBootIdentity
	}

	return snapshot, size, nil
}

func applyBootSwitch(ctx context.Context, client EC2API, modifier instanceAttributeAPI, identity *cpi.BootIdentity, md bootMetadata, root, fromVolumeID, toVolumeID, userdata string) error {
	if root == fromVolumeID {
		if _, err := client.DetachVolume(ctx, &ec2.DetachVolumeInput{InstanceId: aws.String(identity.InstanceID), VolumeId: aws.String(fromVolumeID), Device: aws.String(identity.RootDevice), Force: aws.Bool(false)}); err != nil {
			return err
		}

		if err := ec2.NewVolumeAvailableWaiter(client).Wait(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{fromVolumeID}}, 10*time.Minute); err != nil {
			return err
		}
	}

	if root != toVolumeID {
		if _, err := client.AttachVolume(ctx, &ec2.AttachVolumeInput{InstanceId: aws.String(identity.InstanceID), VolumeId: aws.String(toVolumeID), Device: aws.String(identity.RootDevice)}); err != nil {
			return err
		}
	}

	sm := &StorageManager{}
	if err := sm.waitForAttachment(ctx, client, toVolumeID, identity.InstanceID, identity.RootDevice); err != nil {
		return err
	}

	if _, err := modifier.ModifyInstanceAttribute(ctx, &ec2.ModifyInstanceAttributeInput{InstanceId: aws.String(identity.InstanceID), UserData: &types.SecureBlobAttributeValue{Value: []byte(userdata)}}); err != nil {
		return err
	}

	_, err := modifier.ModifyInstanceAttribute(ctx, &ec2.ModifyInstanceAttributeInput{InstanceId: aws.String(identity.InstanceID), BlockDeviceMappings: []types.InstanceBlockDeviceMappingSpecification{{DeviceName: aws.String(identity.RootDevice), Ebs: &types.EbsInstanceBlockDeviceSpecification{VolumeId: aws.String(toVolumeID), DeleteOnTermination: aws.Bool(md.DeleteOnTermination)}}}})
	if err != nil {
		return err
	}

	return nil
}

func validateBootSwitch(ctx context.Context, client EC2API, i *types.Instance, identity *cpi.BootIdentity, md bootMetadata, fromVolumeID, toVolumeID string) (string, error) {
	root := bootRoot(i)
	if (root != "" && root != fromVolumeID && root != toVolumeID) || i.State == nil || i.State.Name != types.InstanceStateNameStopped {
		return "", errBootIdentity
	}

	userdata := identity.ReplacementUserData
	if toVolumeID == identity.RootVolumeID {
		userdata = md.UserData
	} else if userdata == "" {
		return "", errors.New("replacement guest user data is required")
	}

	target, err := bootVolume(ctx, client, toVolumeID)
	if err != nil {
		return "", err
	}

	if aws.ToString(target.AvailabilityZone) != md.Zone {
		return "", errBootIdentity
	}

	for _, a := range target.Attachments {
		if aws.ToString(a.InstanceId) != identity.InstanceID || aws.ToString(a.Device) != identity.RootDevice {
			return "", errBootIdentity
		}
	}

	return userdata, nil
}

func (m *ComputeManager) RecoverBoot(ctx context.Context, req *cpi.BootRefreshRequest) (*cpi.BootVolume, error) {
	if req == nil || req.Token == "" || req.Identity.InstanceID == "" {
		return nil, errBootIdentity
	}

	client, err := m.getEC2(ctx)
	if err != nil {
		return nil, err
	}

	var (
		found *cpi.BootVolume
		next  *string
	)
	for {
		out, listErr := client.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{Filters: []types.Filter{{Name: aws.String("tag:ocfp:boot-refresh"), Values: []string{req.Token}}, {Name: aws.String("tag:ocfp:instance"), Values: []string{req.Identity.InstanceID}}}, NextToken: next})
		if listErr != nil {
			return nil, listErr
		}

		for _, v := range out.Volumes {
			if found != nil || aws.ToString(v.VolumeId) == req.Identity.RootVolumeID {
				return nil, errBootIdentity
			}

			if !hasBootTag(v.Tags, "ocfp:boot-refresh", req.Token) || !hasBootTag(v.Tags, "ocfp:instance", req.Identity.InstanceID) {
				return nil, errBootIdentity
			}

			id := aws.ToString(v.VolumeId)
			if id == "" {
				return nil, errBootIdentity
			}

			found = &cpi.BootVolume{ID: id, Fingerprint: id}
		}

		next = out.NextToken
		if aws.ToString(next) == "" {
			break
		}
	}

	return found, nil
}
func hasBootTag(tags []types.Tag, key, value string) bool {
	for _, tag := range tags {
		if aws.ToString(tag.Key) == key && aws.ToString(tag.Value) == value {
			return true
		}
	}

	return false
}
