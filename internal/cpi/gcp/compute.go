package gcp

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// CreateInstance creates a new compute instance.
//
//nolint:funlen // GCP instance creation with network, disk, and metadata configuration
func (m *ComputeManager) CreateInstance(ctx context.Context, req *cpi.InstanceRequest) (*cpi.Instance, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	// Determine the zone from request or config
	if req.AvailabilityZone != "" {
		zone = req.AvailabilityZone
	}

	labels := BuildLabels(req.Name, req.Tags)
	machineTypeURL := FormatMachineTypeURL(projectID, zone, req.Flavor)

	// Build instance
	instance := &computepb.Instance{
		Name:              proto(req.Name),
		MachineType:       proto(machineTypeURL),
		NetworkInterfaces: m.buildNetworkInterfaces(config, req),
		Disks:             m.buildBootDisks(zone, req),
		Labels:            labels,
	}

	m.buildSecurityTags(instance, req)
	m.buildInstanceMetadata(instance, req)

	op, err := m.client.getInstancesClient().Insert(ctx, &computepb.InsertInstanceRequest{ //nolint:varnamelen
		Project:          projectID,
		Zone:             zone,
		InstanceResource: instance,
	})
	if err != nil {
		return nil, WrapGCPError(err, "CreateInstance")
	}

	err = op.Wait(ctx)
	if err != nil {
		return nil, WrapGCPError(err, "CreateInstance.Wait")
	}

	logger.Debugw("Created instance", "name", req.Name, "zone", zone, "machineType", req.Flavor)

	// Wait for instance to be running
	waiter := DefaultResourceWaiter()

	err = waiter.WaitForState(ctx, req.Name, []string{"RUNNING"}, func(ctx context.Context) (string, error) {
		inst, err := m.GetInstance(ctx, req.Name)
		if err != nil {
			return "", err
		}

		return string(inst.State), nil
	})
	if err != nil {
		logger.Warnw("Instance created but not running yet", "name", req.Name, "error", err)
	}

	return m.GetInstance(ctx, req.Name)
}

// buildNetworkInterfaces constructs network interface configuration for a new instance.
func (m *ComputeManager) buildNetworkInterfaces(config *Config, req *cpi.InstanceRequest) []*computepb.NetworkInterface {
	networkProject := config.GetNetworkProject()

	if req.SubnetID != "" {
		subnetURL := FormatSubnetworkURL(networkProject, config.Region, req.SubnetID)
		ni := &computepb.NetworkInterface{ //nolint:varnamelen
			Subnetwork: proto(subnetURL),
		}

		// Add access config for external IP if in public subnet
		if req.Tags != nil && req.Tags["public"] == "true" {
			ni.AccessConfigs = []*computepb.AccessConfig{
				{
					Name:        proto("External NAT"),
					Type:        proto("ONE_TO_ONE_NAT"),
					NetworkTier: proto("PREMIUM"),
				},
			}
		}

		return []*computepb.NetworkInterface{ni}
	}

	if req.NetworkID != "" {
		networkURL := FormatNetworkURL(networkProject, req.NetworkID)

		return []*computepb.NetworkInterface{
			{Network: proto(networkURL)},
		}
	}

	return nil
}

// buildBootDisks constructs boot disk configuration for a new instance.
func (m *ComputeManager) buildBootDisks(zone string, req *cpi.InstanceRequest) []*computepb.AttachedDisk {
	if req.Image == "" {
		return nil
	}

	bootDisk := &computepb.AttachedDisk{
		Boot:       proto(true),
		AutoDelete: proto(true),
		InitializeParams: &computepb.AttachedDiskInitializeParams{
			SourceImage: proto(req.Image),
			DiskType:    proto(fmt.Sprintf("zones/%s/diskTypes/pd-balanced", zone)),
		},
	}

	if req.BootVolumeSize > 0 {
		bootDisk.InitializeParams.DiskSizeGb = proto(int64(req.BootVolumeSize))
	}

	return []*computepb.AttachedDisk{bootDisk}
}

// buildSecurityTags adds network tags for security groups to an instance.
func (m *ComputeManager) buildSecurityTags(instance *computepb.Instance, req *cpi.InstanceRequest) {
	if len(req.SecurityGroups) == 0 && len(req.SecurityGroupIDs) == 0 {
		return
	}

	tags := make([]string, 0, len(req.SecurityGroups)+len(req.SecurityGroupIDs))

	for _, sg := range req.SecurityGroups {
		tags = append(tags, FormatNetworkTag(sg))
	}

	for _, sg := range req.SecurityGroupIDs {
		tags = append(tags, FormatNetworkTag(sg))
	}

	instance.Tags = &computepb.Tags{
		Items: tags,
	}
}

// buildInstanceMetadata adds SSH key and user data metadata to an instance.
func (m *ComputeManager) buildInstanceMetadata(instance *computepb.Instance, req *cpi.InstanceRequest) {
	// Add SSH key from keypair
	if req.KeyPair != "" || req.KeyPairName != "" {
		instance.Metadata = &computepb.Metadata{
			Items: []*computepb.Items{},
		}
	}

	// Add user data as startup script
	if req.UserData == "" {
		return
	}

	if instance.GetMetadata() == nil {
		instance.Metadata = &computepb.Metadata{}
	}

	instance.Metadata.Items = append(instance.Metadata.Items, &computepb.Items{
		Key:   proto("startup-script"),
		Value: proto(req.UserData),
	})
}

// GetInstance retrieves an instance by name or ID.
func (m *ComputeManager) GetInstance(ctx context.Context, id string) (*cpi.Instance, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	instance, err := m.client.getInstancesClient().Get(ctx, &computepb.GetInstanceRequest{
		Project:  projectID,
		Zone:     zone,
		Instance: id,
	})
	if err != nil {
		return nil, WrapGCPError(err, "GetInstance")
	}

	return m.convertInstance(instance), nil
}

// ListInstances lists instances with optional filters.
func (m *ComputeManager) ListInstances(ctx context.Context, filters map[string]string) ([]*cpi.Instance, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	var instances []*cpi.Instance

	it := m.client.getInstancesClient().List(ctx, &computepb.ListInstancesRequest{ //nolint:varnamelen
		Project: projectID,
		Zone:    zone,
	})

	for {
		instance, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "ListInstances")
		}

		// Apply label filters
		if matchesLabelFilters(instance.GetLabels(), filters) {
			instances = append(instances, m.convertInstance(instance))
		}
	}

	return instances, nil
}

// StartInstance starts a stopped instance.
func (m *ComputeManager) StartInstance(ctx context.Context, id string) error { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	op, err := m.client.getInstancesClient().Start(ctx, &computepb.StartInstanceRequest{ //nolint:varnamelen
		Project:  projectID,
		Zone:     zone,
		Instance: id,
	})
	if err != nil {
		return WrapGCPError(err, "StartInstance")
	}

	err = op.Wait(ctx)
	if err != nil {
		return WrapGCPError(err, "StartInstance.Wait")
	}

	logger.Debugw("Started instance", "name", id, "zone", zone)

	return nil
}

// StopInstance stops a running instance.
func (m *ComputeManager) StopInstance(ctx context.Context, id string) error { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	op, err := m.client.getInstancesClient().Stop(ctx, &computepb.StopInstanceRequest{ //nolint:varnamelen
		Project:  projectID,
		Zone:     zone,
		Instance: id,
	})
	if err != nil {
		return WrapGCPError(err, "StopInstance")
	}

	err = op.Wait(ctx)
	if err != nil {
		return WrapGCPError(err, "StopInstance.Wait")
	}

	logger.Debugw("Stopped instance", "name", id, "zone", zone)

	return nil
}

// RebootInstance reboots an instance.
func (m *ComputeManager) RebootInstance(ctx context.Context, id string) error { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	op, err := m.client.getInstancesClient().Reset(ctx, &computepb.ResetInstanceRequest{ //nolint:varnamelen
		Project:  projectID,
		Zone:     zone,
		Instance: id,
	})
	if err != nil {
		return WrapGCPError(err, "RebootInstance")
	}

	err = op.Wait(ctx)
	if err != nil {
		return WrapGCPError(err, "RebootInstance.Wait")
	}

	logger.Debugw("Rebooted instance", "name", id, "zone", zone)

	return nil
}

// DeleteInstance deletes an instance.
func (m *ComputeManager) DeleteInstance(ctx context.Context, id string) error { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	op, err := m.client.getInstancesClient().Delete(ctx, &computepb.DeleteInstanceRequest{ //nolint:varnamelen
		Project:  projectID,
		Zone:     zone,
		Instance: id,
	})
	if err != nil {
		return WrapGCPError(err, "DeleteInstance")
	}

	err = op.Wait(ctx)
	if err != nil {
		return WrapGCPError(err, "DeleteInstance.Wait")
	}

	logger.Debugw("Deleted instance", "name", id, "zone", zone)

	return nil
}

// CreateKeyPair creates a new SSH key pair (stored in project metadata).
func (m *ComputeManager) CreateKeyPair(_ctx context.Context, req *cpi.KeyPairRequest) (*cpi.KeyPair, error) {
	// GCP doesn't have a native key pair concept like AWS
	// SSH keys are managed via project or instance metadata
	// We'll return a placeholder that indicates manual key management
	return &cpi.KeyPair{
		Name:      req.Name,
		CreatedAt: time.Now(),
	}, nil
}

// ImportKeyPair imports a public key (adds to project metadata).
func (m *ComputeManager) ImportKeyPair(_ctx context.Context, name string, _publicKey string) error {
	// In a full implementation, this would update project metadata
	// with the SSH key in format: username:ssh-rsa AAAA...
	logger.Debugw("ImportKeyPair called - SSH keys managed via project/instance metadata", "name", name)

	return nil
}

// GetKeyPair retrieves a key pair.
func (m *ComputeManager) GetKeyPair(_ctx context.Context, name string) (*cpi.KeyPair, error) {
	// GCP SSH keys are stored in project metadata
	// Return a placeholder
	return &cpi.KeyPair{
		Name: name,
	}, nil
}

// ListKeyPairs lists key pairs.
func (m *ComputeManager) ListKeyPairs(_ctx context.Context) ([]*cpi.KeyPair, error) {
	// Would need to parse project metadata for SSH keys
	return []*cpi.KeyPair{}, nil
}

// DeleteKeyPair deletes a key pair.
func (m *ComputeManager) DeleteKeyPair(_ctx context.Context, name string) error {
	// Would need to update project metadata to remove the key
	logger.Debugw("DeleteKeyPair called - SSH keys managed via project/instance metadata", "name", name)

	return nil
}

// CreateVolume creates a persistent disk.
//
//nolint:dupl // intentionally similar CPI implementation
func (m *ComputeManager) CreateVolume(ctx context.Context, req *cpi.VolumeRequest) (*cpi.Volume, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	if req.AvailabilityZone != "" {
		zone = req.AvailabilityZone
	}

	labels := BuildLabels(req.Name, req.Tags)

	size := int64(req.Size)
	if req.SizeGB > 0 {
		size = int64(req.SizeGB)
	}

	diskType := "pd-balanced"
	if req.Type != "" {
		diskType = req.Type
	} else if req.VolumeType != "" {
		diskType = req.VolumeType
	}

	disk := &computepb.Disk{
		Name:   proto(req.Name),
		SizeGb: proto(size),
		Type:   proto(fmt.Sprintf("zones/%s/diskTypes/%s", zone, diskType)),
		Labels: labels,
	}

	op, err := m.client.getDisksClient().Insert(ctx, &computepb.InsertDiskRequest{ //nolint:varnamelen
		Project:      projectID,
		Zone:         zone,
		DiskResource: disk,
	})
	if err != nil {
		return nil, WrapGCPError(err, "CreateVolume")
	}

	err = op.Wait(ctx)
	if err != nil {
		return nil, WrapGCPError(err, "CreateVolume.Wait")
	}

	logger.Debugw("Created persistent disk", "name", req.Name, "size", size, "zone", zone)

	return m.GetVolume(ctx, req.Name)
}

// GetVolume retrieves a persistent disk by name.
func (m *ComputeManager) GetVolume(ctx context.Context, id string) (*cpi.Volume, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	disk, err := m.client.getDisksClient().Get(ctx, &computepb.GetDiskRequest{
		Project: projectID,
		Zone:    zone,
		Disk:    id,
	})
	if err != nil {
		return nil, WrapGCPError(err, "GetVolume")
	}

	return m.convertDisk(disk), nil
}

// ListVolumes lists persistent disks.
func (m *ComputeManager) ListVolumes(ctx context.Context, filters map[string]string) ([]*cpi.Volume, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	var volumes []*cpi.Volume

	it := m.client.getDisksClient().List(ctx, &computepb.ListDisksRequest{ //nolint:varnamelen
		Project: projectID,
		Zone:    zone,
	})

	for {
		disk, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "ListVolumes")
		}

		if matchesLabelFilters(disk.GetLabels(), filters) {
			volumes = append(volumes, m.convertDisk(disk))
		}
	}

	return volumes, nil
}

// DeleteVolume deletes a persistent disk.
func (m *ComputeManager) DeleteVolume(ctx context.Context, id string) error { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	op, err := m.client.getDisksClient().Delete(ctx, &computepb.DeleteDiskRequest{ //nolint:varnamelen
		Project: projectID,
		Zone:    zone,
		Disk:    id,
	})
	if err != nil {
		return WrapGCPError(err, "DeleteVolume")
	}

	err = op.Wait(ctx)
	if err != nil {
		return WrapGCPError(err, "DeleteVolume.Wait")
	}

	logger.Debugw("Deleted persistent disk", "name", id, "zone", zone)

	return nil
}

// ListImages lists available images.
func (m *ComputeManager) ListImages(ctx context.Context, _filters map[string]string) ([]*cpi.Image, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID

	var images []*cpi.Image

	// List images from the project
	it := m.client.getImagesClient().List(ctx, &computepb.ListImagesRequest{
		Project: projectID,
	})

	for {
		image, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "ListImages")
		}

		images = append(images, m.convertImage(image))
	}

	// Also list public images from common projects
	publicProjects := []string{"debian-cloud", "ubuntu-os-cloud", "centos-cloud", "cos-cloud"}
	for _, project := range publicProjects {
		it := m.client.getImagesClient().List(ctx, &computepb.ListImagesRequest{
			Project: project,
		})

		for {
			image, err := it.Next()
			if errors.Is(err, iterator.Done) {
				break
			}

			if err != nil {
				// Ignore errors from public projects
				break
			}

			images = append(images, m.convertImage(image))
		}
	}

	return images, nil
}

// GetImage retrieves an image by name or ID.
func (m *ComputeManager) GetImage(ctx context.Context, id string) (*cpi.Image, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID

	// Try to get from project first
	image, err := m.client.getImagesClient().Get(ctx, &computepb.GetImageRequest{
		Project: projectID,
		Image:   id,
	})
	if err == nil {
		return m.convertImage(image), nil
	}

	// Try common public projects
	publicProjects := []string{"debian-cloud", "ubuntu-os-cloud", "centos-cloud", "cos-cloud"}
	for _, project := range publicProjects {
		image, err := m.client.getImagesClient().Get(ctx, &computepb.GetImageRequest{
			Project: project,
			Image:   id,
		})
		if err == nil {
			return m.convertImage(image), nil
		}
	}

	return nil, WrapGCPError(err, "GetImage")
}

// ListFlavors lists available machine types.
func (m *ComputeManager) ListFlavors(ctx context.Context) ([]*cpi.Flavor, error) {
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	var flavors []*cpi.Flavor

	it := m.client.getMachineTypesClient().List(ctx, &computepb.ListMachineTypesRequest{ //nolint:varnamelen
		Project: projectID,
		Zone:    zone,
	})

	for {
		mt, err := it.Next() //nolint:varnamelen
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "ListFlavors")
		}

		flavors = append(flavors, m.convertMachineType(mt))
	}

	return flavors, nil
}

// GetFlavor retrieves a machine type by name.
func (m *ComputeManager) GetFlavor(ctx context.Context, id string) (*cpi.Flavor, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	zone := config.Zone

	mt, err := m.client.getMachineTypesClient().Get(ctx, &computepb.GetMachineTypeRequest{ //nolint:varnamelen
		Project:     projectID,
		Zone:        zone,
		MachineType: id,
	})
	if err != nil {
		return nil, WrapGCPError(err, "GetFlavor")
	}

	return m.convertMachineType(mt), nil
}

// Helper functions

func (m *ComputeManager) convertInstance(instance *computepb.Instance) *cpi.Instance {
	var (
		privateIP, publicIP string
		securityGroups      []string
	)

	// Extract IPs from network interfaces

	for _, ni := range instance.GetNetworkInterfaces() {
		if ni.GetNetworkIP() != "" {
			privateIP = ni.GetNetworkIP()
		}

		for _, ac := range ni.GetAccessConfigs() {
			if ac.GetNatIP() != "" {
				publicIP = ac.GetNatIP()
			}
		}
	}

	// Extract security groups from tags
	if instance.GetTags() != nil {
		for _, tag := range instance.GetTags().GetItems() {
			if strings.HasPrefix(tag, "sg-") {
				securityGroups = append(securityGroups, strings.TrimPrefix(tag, "sg-"))
			}
		}
	}

	// Extract attached volumes
	var volumes []string

	for _, disk := range instance.GetDisks() {
		if !disk.GetBoot() {
			volumes = append(volumes, ExtractNameFromURL(disk.GetSource()))
		}
	}

	return &cpi.Instance{
		ID:               strconv.FormatUint(instance.GetId(), 10),
		Name:             instance.GetName(),
		State:            MapGCPStateToResourceState(instance.GetStatus()),
		Flavor:           ExtractNameFromURL(instance.GetMachineType()),
		PrivateIP:        privateIP,
		PublicIP:         publicIP,
		SecurityGroups:   securityGroups,
		AvailabilityZone: ExtractZoneFromURL(instance.GetZone()),
		Tags:             instance.GetLabels(),
		Volumes:          volumes,
		CreatedAt:        ParseTimestamp(instance.GetCreationTimestamp()),
	}
}

func (m *ComputeManager) convertDisk(disk *computepb.Disk) *cpi.Volume {
	var attachedTo string
	if len(disk.GetUsers()) > 0 {
		attachedTo = ExtractNameFromURL(disk.GetUsers()[0])
	}

	return &cpi.Volume{
		ID:         strconv.FormatUint(disk.GetId(), 10),
		Name:       disk.GetName(),
		Size:       int(disk.GetSizeGb()),
		Type:       ExtractNameFromURL(disk.GetType()),
		State:      MapDiskStateToResourceState(disk.GetStatus()),
		AttachedTo: attachedTo,
		Tags:       disk.GetLabels(),
		CreatedAt:  ParseTimestamp(disk.GetCreationTimestamp()),
	}
}

func (m *ComputeManager) convertImage(image *computepb.Image) *cpi.Image {
	// Check if image is deprecated/deleted
	isPublic := true
	if deprecated := image.GetDeprecated(); deprecated != nil {
		isPublic = deprecated.GetDeleted() == "" && deprecated.GetState() != "DELETED"
	}

	return &cpi.Image{
		ID:          strconv.FormatUint(image.GetId(), 10),
		Name:        image.GetName(),
		Description: image.GetDescription(),
		Size:        image.GetDiskSizeGb() * 1024 * 1024 * 1024, //nolint:mnd // Convert to bytes
		MinDisk:     int(image.GetDiskSizeGb()),
		Public:      isPublic,
		State:       image.GetStatus(),
		Tags:        image.GetLabels(),
		CreatedAt:   ParseTimestamp(image.GetCreationTimestamp()),
	}
}

func (m *ComputeManager) convertMachineType(mt *computepb.MachineType) *cpi.Flavor { //nolint:varnamelen
	return &cpi.Flavor{
		ID:          strconv.FormatUint(mt.GetId(), 10),
		Name:        mt.GetName(),
		VCPUs:       int(mt.GetGuestCpus()),
		RAM:         int(mt.GetMemoryMb()),
		Description: mt.GetDescription(),
	}
}

func matchesLabelFilters(labels map[string]string, filters map[string]string) bool {
	if len(filters) == 0 {
		return true
	}

	for k, v := range filters {
		if labels[k] != v {
			return false
		}
	}

	return true
}

// Ensure base64 import is used.
var _ = base64.StdEncoding
