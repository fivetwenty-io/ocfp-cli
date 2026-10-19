package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
)

// CreateInstance creates a new compute instance.
//
//nolint:funlen // GCP instance creation with network, disk, and metadata configuration
func (m *ComputeManager) CreateInstance(ctx context.Context, req *cpi.InstanceRequest) (*cpi.Instance, error) {
	if req == nil {
		return nil, errors.New("instance request is required")
	}

	if req.VCPUsOverride != 0 || req.MemoryMiBOverride != 0 {
		return nil, fmt.Errorf("%w: GCP compute sizing uses Flavor; select a custom machine type instead of PVE CPU/RAM overrides", ErrNotImplemented)
	}

	if req.CreateStopped {
		return nil, fmt.Errorf("%w: GCP cannot atomically create a stopped instance; safe recycle requires staged identity handover", ErrNotImplemented)
	}

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

	// Resolve legacy numeric handles before constructing URLs and network tags.
	request := *req
	request.SecurityGroups = append([]string(nil), req.SecurityGroups...)
	request.SecurityGroupIDs = append([]string(nil), req.SecurityGroupIDs...)

	req = &request
	for _, groups := range [][]string{req.SecurityGroups, req.SecurityGroupIDs} {
		for i, id := range groups {
			if isNumericResource(id) {
				group, err := (&SecurityManager{client: m.client}).GetSecurityGroup(ctx, id)
				if err != nil {
					return nil, err
				}

				groups[i] = group.ID
			}
		}
	}

	if isNumericResource(req.NetworkID) {
		network, err := (&NetworkManager{client: m.client}).GetNetwork(ctx, req.NetworkID)
		if err != nil {
			return nil, err
		}

		req.NetworkID = network.ID
	}

	if isNumericResource(req.SubnetID) {
		subnet, err := (&NetworkManager{client: m.client}).GetSubnet(ctx, req.SubnetID)
		if err != nil {
			return nil, err
		}

		req.SubnetID = subnet.ID
	}

	labels := BuildLabels(req.Name, req.Tags)
	machineTypeURL := FormatMachineTypeURL(projectID, zone, req.Flavor)

	// Build instance
	instance := &computepb.Instance{
		Name:               proto(req.Name),
		MachineType:        proto(machineTypeURL),
		NetworkInterfaces:  m.buildNetworkInterfaces(config, req),
		Disks:              m.buildBootDisks(zone, req),
		Labels:             labels,
		DeletionProtection: proto(req.Protected),
		CanIpForward:       proto(req.IPForwarding || req.Tailscale != nil || req.Ingress != nil),
	}

	m.buildSecurityTags(instance, req)
	m.buildInstanceMetadata(instance, req)

	fingerprint, err := json.Marshal(instance)
	if err != nil {
		return nil, fmt.Errorf("encode instance fingerprint: %w", err)
	}

	digest := sha256.Sum256(fingerprint)
	instance.Labels["ocfp-request-sha256"] = hex.EncodeToString(digest[:31])

	op, err := m.client.getInstancesClient().Insert(ctx, &computepb.InsertInstanceRequest{ //nolint:varnamelen
		Project:          projectID,
		Zone:             zone,
		InstanceResource: instance,
	})
	if err != nil {
		wrapped := WrapGCPError(err, "CreateInstance")
		if !IsAlreadyExists(wrapped) {
			return nil, wrapped
		}

		existing, getErr := m.client.getInstancesClient().Get(ctx, &computepb.GetInstanceRequest{Project: projectID, Zone: zone, Instance: req.Name})
		if getErr != nil {
			return nil, WrapGCPError(getErr, "CreateInstance.Reconcile")
		}

		if !instanceMatchesRequest(existing, instance, req) {
			return nil, fmt.Errorf("existing instance %s does not match requested ownership/configuration", req.Name)
		}

		if existing.GetStatus() != "RUNNING" {
			return m.convertInstance(existing), fmt.Errorf("existing instance %s is not running", req.Name)
		}

		return m.convertInstance(existing), nil
	}

	err = op.Wait(ctx)
	if err != nil {
		return &cpi.Instance{ID: scopedResource(projectID, zone, "instances", req.Name), Name: req.Name}, WrapGCPError(err, "CreateInstance.Wait")
	}

	logger.Debugw("Created instance", "name", req.Name, "zone", zone, "machineType", req.Flavor)

	// Wait for instance to be running
	waiter := DefaultResourceWaiter()

	err = waiter.WaitForState(ctx, req.Name, []string{string(cpi.ResourceStateActive)}, func(ctx context.Context) (string, error) {
		inst, err := m.GetInstance(ctx, scopedResource(projectID, zone, "instances", req.Name))
		if err != nil {
			return "", err
		}

		return string(inst.State), nil
	})
	if err != nil {
		return &cpi.Instance{ID: scopedResource(projectID, zone, "instances", req.Name), Name: req.Name}, fmt.Errorf("instance %s created but readiness failed: %w", req.Name, err)
	}

	return m.GetInstance(ctx, scopedResource(projectID, zone, "instances", req.Name))
}

// buildNetworkInterfaces constructs network interface configuration for a new instance.
func (m *ComputeManager) buildNetworkInterfaces(config *Config, req *cpi.InstanceRequest) []*computepb.NetworkInterface {
	networkProject := config.GetNetworkProject()

	if req.SubnetID != "" {
		subnetURL := FormatSubnetworkURL(networkProject, config.Region, req.SubnetID)
		ni := &computepb.NetworkInterface{ //nolint:varnamelen
			Subnetwork: proto(subnetURL),
			NetworkIP:  proto(req.StaticPrivateIP),
		}

		// Add access config for external IP if in public subnet
		if (req.PublicIP != nil && *req.PublicIP) || (req.PublicIP == nil && req.Tags["public"] == "true") {
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

		nic := &computepb.NetworkInterface{Network: proto(networkURL), NetworkIP: proto(req.StaticPrivateIP)}
		if (req.PublicIP != nil && *req.PublicIP) || (req.PublicIP == nil && req.Tags["public"] == "true") {
			nic.AccessConfigs = []*computepb.AccessConfig{{Name: proto("External NAT"), Type: proto("ONE_TO_ONE_NAT"), NetworkTier: proto("PREMIUM")}}
		}

		return []*computepb.NetworkInterface{nic}
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
		tags = append(tags, FormatNetworkTag(ExtractNameFromURL(sg)))
	}

	for _, sg := range req.SecurityGroupIDs {
		tags = append(tags, FormatNetworkTag(ExtractNameFromURL(sg)))
	}

	instance.Tags = &computepb.Tags{
		Items: tags,
	}
}

// buildInstanceMetadata adds SSH key and user data metadata to an instance.
func (m *ComputeManager) buildInstanceMetadata(instance *computepb.Instance, req *cpi.InstanceRequest) {
	instance.Metadata = &computepb.Metadata{}

	if req.PublicKey != "" {
		username := req.DefaultUsername
		if username == "" {
			username = "vcap"
		}

		instance.Metadata.Items = append(instance.Metadata.Items, &computepb.Items{Key: proto("ssh-keys"), Value: proto(username + ":" + strings.TrimSpace(req.PublicKey))}, &computepb.Items{Key: proto("block-project-ssh-keys"), Value: proto("true")})
	}

	if req.UserData != "" {
		key := "startup-script"
		if strings.HasPrefix(strings.TrimSpace(req.UserData), "#cloud-config") || strings.HasPrefix(strings.TrimSpace(req.UserData), "Content-Type: multipart/") {
			key = "user-data"
		}

		instance.Metadata.Items = append(instance.Metadata.Items, &computepb.Items{Key: proto(key), Value: proto(req.UserData)})
	}
}

// GetInstance retrieves an instance by name or ID.
func (m *ComputeManager) GetInstance(ctx context.Context, id string) (*cpi.Instance, error) { //nolint:varnamelen
	err := m.client.ensureClientsLoaded(ctx)
	if err != nil {
		return nil, err
	}

	config := m.client.getConfig()
	projectID := config.ProjectID
	projectID, zone, id := resourceScope(projectID, config.Zone, id)

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
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return nil, err
	}

	it := m.client.getInstancesClient().AggregatedList(ctx, &computepb.AggregatedListInstancesRequest{Project: m.client.getConfig().ProjectID})
	instances := []*cpi.Instance{}

	for {
		pair, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "ListInstances")
		}

		for _, instance := range pair.Value.GetInstances() {
			if matchesLabelFilters(instance.GetLabels(), filters) {
				instances = append(instances, m.convertInstance(instance))
			}
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
	projectID, zone, id := resourceScope(projectID, config.Zone, id)

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
	projectID, zone, id := resourceScope(projectID, config.Zone, id)

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
	projectID, zone, id := resourceScope(projectID, config.Zone, id)

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
	projectID, zone, id := resourceScope(projectID, config.Zone, id)

	// ocfp deletion is an explicit lifecycle operation; clear the console guard
	// only after locating this exact instance and preserve lookup/permission errors.
	vm, lookupErr := m.client.getInstancesClient().Get(ctx, &computepb.GetInstanceRequest{Project: projectID, Zone: zone, Instance: id})
	if lookupErr != nil {
		wrapped := WrapGCPError(lookupErr, "DeleteInstance.Get")
		if IsNotFound(wrapped) {
			return nil
		}

		return wrapped
	}

	if vm.GetDeletionProtection() {
		if clearErr := m.EnsureGuestOptions(ctx, scopedResource(projectID, zone, "instances", id), cpi.GuestOptions{}); clearErr != nil {
			return clearErr
		}
	}

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
func (m *ComputeManager) CreateKeyPair(_ context.Context, _ *cpi.KeyPairRequest) (*cpi.KeyPair, error) {
	return nil, fmt.Errorf("%w: GCP uses locally managed keys injected per instance", ErrNotImplemented)
}
func (m *ComputeManager) ImportKeyPair(_ context.Context, _ string, publicKey string) error {
	if strings.TrimSpace(publicKey) == "" {
		return errors.New("public key is required")
	}

	return nil
}
func (m *ComputeManager) GetKeyPair(_ context.Context, name string) (*cpi.KeyPair, error) {
	return nil, &cpi.ProviderError{Provider: "gcp", Code: "NotFound", Message: "GCP keys are managed locally: " + name}
}
func (m *ComputeManager) ListKeyPairs(_ context.Context) ([]*cpi.KeyPair, error) {
	return []*cpi.KeyPair{}, nil
}
func (m *ComputeManager) DeleteKeyPair(_ context.Context, _ string) error { return nil }

// CreateVolume creates a persistent disk.
//
//nolint:dupl // intentionally similar CPI implementation
func (m *ComputeManager) CreateVolume(ctx context.Context, req *cpi.VolumeRequest) (*cpi.Volume, error) {
	return (&StorageManager{client: m.client}).CreateVolume(ctx, req)
}

// GetVolume retrieves a persistent disk by name.
func (m *ComputeManager) GetVolume(ctx context.Context, id string) (*cpi.Volume, error) {
	return (&StorageManager{client: m.client}).GetVolume(ctx, id)
}

// ListVolumes lists persistent disks.
func (m *ComputeManager) ListVolumes(ctx context.Context, filters map[string]string) ([]*cpi.Volume, error) {
	return (&StorageManager{client: m.client}).ListVolumes(ctx, filters)
}

// DeleteVolume deletes a persistent disk.
func (m *ComputeManager) DeleteVolume(ctx context.Context, id string) error {
	return (&StorageManager{client: m.client}).DeleteVolume(ctx, id)
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
func (m *ComputeManager) GetImage(ctx context.Context, id string) (*cpi.Image, error) {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return nil, err
	}

	project, _, name := resourceScope(m.client.getConfig().ProjectID, "", id)
	scoped := strings.Contains(id, "projects/")
	lookup := func(project string) (*cpi.Image, error) {
		var (
			image *computepb.Image
			err   error
		)
		if strings.Contains(id, "/images/family/") {
			image, err = m.client.getImagesClient().GetFromFamily(ctx, &computepb.GetFromFamilyImageRequest{Project: project, Family: name})
		} else {
			image, err = m.client.getImagesClient().Get(ctx, &computepb.GetImageRequest{Project: project, Image: name})
		}

		if err != nil {
			return nil, WrapGCPError(err, "GetImage")
		}

		if image.GetSelfLink() == "" {
			image.SelfLink = proto(fmt.Sprintf("projects/%s/global/images/%s", project, image.GetName()))
		}

		return m.convertImage(image), nil
	}

	image, err := lookup(project)
	if err == nil || scoped || !IsNotFound(err) {
		return image, err
	}

	for _, publicProject := range []string{"debian-cloud", "ubuntu-os-cloud", "centos-cloud", "cos-cloud"} {
		image, err = lookup(publicProject)
		if err == nil || !IsNotFound(err) {
			return image, err
		}
	}

	return nil, err
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
	projectID, zone, id = resourceScope(projectID, zone, id)

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
		networkID, subnetID string
		privateIP, publicIP string
		securityGroups      []string
	)

	// Extract IPs from network interfaces

	for _, ni := range instance.GetNetworkInterfaces() {
		if networkID == "" {
			networkID = ExtractNameFromURL(ni.GetNetwork())
			subnetID = ExtractNameFromURL(ni.GetSubnetwork())
		}

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
		ID:        resourceIdentity(m.client.getConfig(), instance.GetZone(), "instances", instance.GetName()),
		Name:      instance.GetName(),
		State:     MapGCPStateToResourceState(instance.GetStatus()),
		Flavor:    ExtractNameFromURL(instance.GetMachineType()),
		PrivateIP: privateIP,
		NetworkID: networkID, SubnetID: subnetID,
		SecurityGroupIDs: securityGroups,
		PublicIP:         publicIP,
		SecurityGroups:   securityGroups,
		AvailabilityZone: ExtractZoneFromURL(instance.GetZone()),
		Tags:             decodedLabels(instance.GetLabels()),
		Volumes:          volumes,
		CreatedAt:        ParseTimestamp(instance.GetCreationTimestamp()),
	}
}

func (m *ComputeManager) convertDisk(disk *computepb.Disk) *cpi.Volume {
	return (&StorageManager{client: m.client}).convertDisk(disk)
}

func (m *ComputeManager) convertImage(image *computepb.Image) *cpi.Image {
	// Check if image is deprecated/deleted
	isPublic := true
	if deprecated := image.GetDeprecated(); deprecated != nil {
		isPublic = deprecated.GetDeleted() == "" && deprecated.GetState() != "DELETED"
	}

	return &cpi.Image{
		ID:          image.GetSelfLink(),
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
		ID:          mt.GetName(),
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
		cleanKey := SanitizeLabel(stripLabelPrefix(k))
		if labels[cleanKey] != SanitizeLabel(v) {
			return false
		}
	}

	return true
}

// EnsureGuestOptions converges native deletion protection. GCE already restarts
// ordinary instances after infrastructure failures; StartOnBoot is PVE-specific.
func (m *ComputeManager) EnsureGuestOptions(ctx context.Context, id string, options cpi.GuestOptions) error {
	if err := m.client.ensureClientsLoaded(ctx); err != nil {
		return err
	}

	cfg := m.client.getConfig()
	project, zone, name := resourceScope(cfg.ProjectID, cfg.Zone, id)

	op, err := m.client.getInstancesClient().SetDeletionProtection(ctx, &computepb.SetDeletionProtectionInstanceRequest{Project: project, Zone: zone, Resource: name, DeletionProtection: proto(options.Protected)})
	if err != nil {
		return WrapGCPError(err, "EnsureGuestOptions")
	}

	return WrapGCPError(op.Wait(ctx), "EnsureGuestOptions.Wait")
}

// Adoption is deliberately conservative: ownership and each requested native
// setting must agree; a conflict never authorizes changing a running guest.
func instanceMatchesRequest(existing, desired *computepb.Instance, req *cpi.InstanceRequest) bool {
	if len(req.Tags) == 0 || !matchesLabelFilters(existing.GetLabels(), req.Tags) || ExtractNameFromURL(existing.GetMachineType()) != req.Flavor || existing.GetDeletionProtection() != desired.GetDeletionProtection() || existing.GetCanIpForward() != desired.GetCanIpForward() {
		return false
	}

	if !instanceNetworksMatch(existing.GetNetworkInterfaces(), desired.GetNetworkInterfaces()) {
		return false
	}

	metadata := map[string]string{}
	for _, item := range existing.GetMetadata().GetItems() {
		metadata[item.GetKey()] = item.GetValue()
	}

	for _, item := range desired.GetMetadata().GetItems() {
		if metadata[item.GetKey()] != item.GetValue() {
			return false
		}
	}

	if !sameStringSet(existing.GetTags().GetItems(), desired.GetTags().GetItems()) {
		return false
	}
	// The creation fingerprint binds immutable image and disk requests even
	// though the read API omits boot-disk initialize parameters.
	if req.Image != "" || req.BootVolumeSize > 0 {
		if existing.GetLabels()["ocfp-request-sha256"] != desired.GetLabels()["ocfp-request-sha256"] {
			return false
		}
	}

	return true
}
func canonicalURL(value string) string {
	return strings.TrimPrefix(value, "https://www.googleapis.com/compute/v1/")
}
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	counts := map[string]int{}
	for _, v := range a {
		counts[v]++
	}

	for _, v := range b {
		counts[v]--
	}

	for _, v := range counts {
		if v != 0 {
			return false
		}
	}

	return true
}

func instanceNetworksMatch(existing, desired []*computepb.NetworkInterface) bool {
	if len(existing) != len(desired) {
		return false
	}

	for i, nic := range desired {
		actual := existing[i]
		if nic.GetSubnetwork() != "" && canonicalURL(actual.GetSubnetwork()) != canonicalURL(nic.GetSubnetwork()) {
			return false
		}

		if nic.GetNetwork() != "" && canonicalURL(actual.GetNetwork()) != canonicalURL(nic.GetNetwork()) {
			return false
		}

		if nic.GetNetworkIP() != "" && actual.GetNetworkIP() != nic.GetNetworkIP() {
			return false
		}

		if len(actual.GetAccessConfigs()) != len(nic.GetAccessConfigs()) {
			return false
		}
	}

	return true
}
