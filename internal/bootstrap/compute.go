package bootstrap

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/bastion/ssh"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	pveclient "github.com/ocfp/ocfp-cli-go/internal/cpi/pve"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/ocfp/ocfp-cli-go/internal/vault"
)

// bastionTailscaleDefaultTag is the ACL tag bastions advertise when the
// operator hasn't customised tailscale.tags. Kept here so ACL templates in
// docs can refer to the same value the resolver emits.
const bastionTailscaleDefaultTag = "tag:ocfp-bastion"

// Compute-specific constants.
const (
	bastionRootDiskSize   = 50
	bastionDataDiskSize   = 100
	maxDebugImagesDisplay = 5
	sshKeyDirMode         = 0700
	sshKeyFileMode        = 0600
)

// bastionSubnetInfo holds subnet information for bastion instance.
type bastionSubnetInfo struct {
	ID               string
	Name             string
	CIDR             string
	AvailabilityZone string
}

// ==============================================================================
// Error Helper Functions
// ==============================================================================

// errImageNotFoundWithName wraps the static error with the image name.
func errImageNotFoundWithName(name string) error {
	return fmt.Errorf("%w: %s", errImageNotFound, name)
}

// errNoSuitableFlavorWithDisk wraps the static error with disk requirement.
func errNoSuitableFlavorWithDisk(diskGB int) error {
	return fmt.Errorf("%w (diskless or with at least %d GB disk space)", errNoSuitableFlavor, diskGB)
}

// errNoFlavorWithDiskSize wraps the static error with disk requirement.
func errNoFlavorWithDiskSize(diskGB int) error {
	return fmt.Errorf("%w: at least %d GB required", errNoFlavorWithDisk, diskGB)
}

// ==============================================================================
// Bastion Creation
// ==============================================================================

// CreateBastion creates the bastion host.
func (m *Manager) CreateBastion(ctx context.Context) error {
	bastionName := m.options.BlocName + "-bastion"

	// Check if bastion already exists
	if m.bastionAlreadyExists(ctx, bastionName) {
		_, _ = fmt.Fprintf(os.Stdout, "    • Bastion %s already exists, skipping\n", bastionName)
		logger.Infof("Bastion %s already exists, skipping creation", bastionName)

		return nil
	}

	// Resolve networking
	_, _ = fmt.Fprintf(os.Stdout, "    • Creating bastion instance %s...\n", bastionName)

	networkID, subnetInfo, err := m.resolveBastionNetworking()
	if err != nil {
		return fmt.Errorf("failed to resolve bastion networking: %w", err)
	}

	// Get security group
	sgID, err := m.getBastionSecurityGroup()
	if err != nil {
		return fmt.Errorf("failed to get bastion security group: %w", err)
	}

	// Create bastion instance
	instance, err := m.createBastionInstance(ctx, bastionName, networkID, subnetInfo.ID, subnetInfo.AvailabilityZone, sgID)
	if err != nil {
		return fmt.Errorf("failed to create bastion instance: %w", err)
	}

	// Save to state
	err = m.saveBastionToState(instance, bastionName)
	if err != nil {
		return fmt.Errorf("failed to save bastion to state: %w", err)
	}

	// Attach public IP for STACKIT
	if strings.EqualFold(m.options.Provider, "stackit") {
		err = m.attachBastionPublicIP(ctx, instance.ID, bastionName)
		if err != nil {
			logger.Warnf("Failed to attach public IP to bastion: %v", err)
			// Don't fail the entire bootstrap if public IP attachment fails
		} else {
			// Wait for network configuration to stabilize and propagate
			// STACKIT needs time for:
			// - NIC/public IP association to complete
			// - Network routing tables to update
			// - Instance network stack to recognize the new IP
			logger.Info("Waiting 30 seconds for network configuration to propagate...")

			_, _ = fmt.Fprintf(os.Stdout, "    • Waiting for network configuration to stabilize...\n")

			time.Sleep(30 * time.Second) //nolint:mnd
			logger.Info("Network wait period completed")
		}
	}

	logger.Infof("Bastion created successfully: id=%s name=%s", instance.ID, bastionName)

	return nil
}

func (m *Manager) bastionAlreadyExists(ctx context.Context, bastionName string) bool {
	// First check state file (fast path)
	existingBastion, _ := m.stateManager.GetResource(state.ResourceTypeInstance, bastionName)
	if existingBastion != nil {
		return true
	}

	// Check cloud provider to catch orphaned resources from failed bootstraps
	// This prevents creating duplicate bastions when state is lost/corrupted
	logger.Debugf("Bastion not found in state, checking cloud provider for %s", bastionName)

	computeMgr := m.provider.ComputeManager()

	// List instances with OCFP tags to find potential orphaned bastions
	instances, err := computeMgr.ListInstances(ctx, map[string]string{
		"bloc":       m.options.BlocName,
		"managed-by": "ocfp",
	})
	if err != nil {
		logger.Warnf("Failed to list instances from cloud provider: %v", err)
		// Continue anyway - better to potentially create duplicate than fail bootstrap
		return false
	}

	// Check if any instance matches the bastion name
	for _, inst := range instances {
		if inst.Name == bastionName {
			logger.Warnf("Found existing bastion in cloud but not in state: %s (ID: %s)", bastionName, inst.ID)
			logger.Warnf("This may be from a previous failed bootstrap. Importing to state to prevent duplicate creation.")

			// Import the orphaned bastion to state
			err = m.importBastionToState(inst, bastionName)
			if err != nil {
				logger.Errorf("Failed to import orphaned bastion to state: %v", err)
				// Still return true to prevent duplicate creation
			}

			return true
		}
	}

	return false
}

func (m *Manager) importBastionToState(instance *cpi.Instance, bastionName string) error {
	// Import the bastion instance to state to prevent future duplicate creation
	logger.Infof("Importing orphaned bastion to state: %s (ID: %s)", bastionName, instance.ID)

	err := m.stateManager.AddResource(&state.Resource{
		ID:       instance.ID,
		Type:     state.ResourceTypeInstance,
		Name:     bastionName,
		Provider: m.options.Provider,
		State:    string(cpi.ResourceStateActive),
		Properties: map[string]interface{}{
			"flavor":          instance.Flavor,
			"image":           instance.Image,
			"private_ip":      instance.PrivateIP,
			"public_ip":       instance.PublicIP,
			"keypair":         instance.KeyPair,
			"subnet_id":       instance.SubnetID,
			"security_groups": instance.SecurityGroups,
		},
		Tags:      m.baseTags(),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	if err != nil {
		return fmt.Errorf("failed to add bastion to state: %w", err)
	}

	// Add dependencies
	subnetName := m.getDefaultSubnetName()
	m.addBastionDependencies(bastionName, subnetName)

	// Save outputs
	m.saveBastionOutputs(instance)

	// Save state immediately
	err = m.stateManager.Save()
	if err != nil {
		return fmt.Errorf("failed to save state after importing bastion: %w", err)
	}

	logger.Infof("Successfully imported orphaned bastion to state: %s", bastionName)

	return nil
}

func (m *Manager) resolveBastionNetworking() (string, *bastionSubnetInfo, error) {
	// Get network ID from outputs
	networkOutput, err := m.stateManager.GetOutput("network_id")
	if err != nil {
		return "", nil, fmt.Errorf("failed to get network ID: %w", err)
	}

	networkID, ok := networkOutput.(string)
	if !ok || networkID == "" {
		return "", nil, ErrInvalidNetworkID(networkOutput)
	}

	// Try to find bastion subnet
	subnetInfo, err := m.findBastionSubnet()
	if err != nil {
		logger.Warnf("Failed to find bastion subnet: %v", err)

		// Try fallback subnet
		subnetInfo, err = m.findFallbackSubnet()
		if err != nil {
			return "", nil, fmt.Errorf("failed to find any suitable subnet for bastion: %w", err)
		}

		logger.Infof("Using fallback subnet for bastion: %s", subnetInfo.Name)
	}

	return networkID, subnetInfo, nil
}

func (m *Manager) findBastionSubnet() (*bastionSubnetInfo, error) {
	// PVE blocs carve a dedicated infra subnet that owns the bastion,
	// director, and shared service reservations. Prefer that subnet when
	// running on PVE; fall through to the legacy ocfp-0 lookup if the
	// infra subnet is absent (mixed-mode upgrades, partial state, etc.).
	if m.useVirtualSubnetsForPVE() {
		infraName := m.options.BlocName + pveInfraSubnetSuffix
		if info, ok := m.lookupSubnetByName(infraName); ok {
			return info, nil
		}
	}

	bastionSubnet := m.options.BlocName + "-ocfp-0"
	if info, ok := m.lookupSubnetByName(bastionSubnet); ok {
		return info, nil
	}

	return nil, ErrBastionSubnetNotFound(bastionSubnet)
}

// lookupSubnetByName resolves a subnet from state by name, returning a
// populated bastionSubnetInfo when present. The availability zone falls
// back to the first configured/default AZ when the stored value is empty,
// which mirrors the original findBastionSubnet behavior.
func (m *Manager) lookupSubnetByName(name string) (*bastionSubnetInfo, bool) {
	subnet, _ := m.stateManager.GetResource("subnet", name)
	if subnet == nil {
		return nil, false
	}

	cidr, ok := subnet.Properties["cidr"].(string)
	if !ok {
		return nil, false
	}

	availabilityZone, _ := subnet.Properties["availability_zone"].(string)
	if availabilityZone == "" {
		availabilityZone = m.getFirstAvailabilityZone()
	}

	return &bastionSubnetInfo{
		ID:               subnet.ID,
		CIDR:             cidr,
		Name:             name,
		AvailabilityZone: availabilityZone,
	}, true
}

func (m *Manager) findFallbackSubnet() (*bastionSubnetInfo, error) {
	// Get all subnets from state
	resources, err := m.stateManager.GetResourcesByType("subnet")
	if err != nil {
		return nil, fmt.Errorf("failed to get subnets from state: %w", err)
	}

	// Find first subnet belonging to this bloc
	for _, resource := range resources {
		if strings.HasPrefix(resource.Name, m.options.BlocName+"-") {
			m.saveFallbackAsManagementSubnet(resource.ID)

			cidr, ok := resource.Properties["cidr"].(string)
			if !ok {
				return nil, ErrInvalidCIDRTypeForResource(resource.Name)
			}

			// Extract availability zone from subnet properties, fallback to default if not set
			availabilityZone, _ := resource.Properties["availability_zone"].(string)
			if availabilityZone == "" {
				availabilityZone = m.getFirstAvailabilityZone()
			}

			return &bastionSubnetInfo{
				ID:               resource.ID,
				CIDR:             cidr,
				Name:             resource.Name,
				AvailabilityZone: availabilityZone,
			}, nil
		}
	}

	return nil, ErrNoSuitableSubnetFoundForBastion
}

func (m *Manager) saveFallbackAsManagementSubnet(subnetID string) {
	_ = m.stateManager.SetOutput("mgmt_subnet_id", subnetID)
}

// getAvailabilityZone returns the availability zone for a given index from config or region-based default.
// When config AZs are defined (e.g., AWS: us-east-1a, us-east-1b), returns the sorted AZ at the given index.
// Falls back to STACKIT-style numeric suffix (e.g., eu01-1) when config AZs are absent or index is out of range.
func (m *Manager) getAvailabilityZone(index int) string {
	if len(m.config.AZs) > 0 {
		azNames := make([]string, 0, len(m.config.AZs))
		for azName := range m.config.AZs {
			azNames = append(azNames, azName)
		}

		sort.Strings(azNames)

		if index < len(azNames) {
			return azNames[index]
		}
	}

	// Fallback: provider-aware AZ formatting
	region := m.options.Region
	if region == "" {
		region = "eu01"
	}

	return config.FormatAvailabilityZone(m.options.Provider, region, index)
}

// getFirstAvailabilityZone returns the first availability zone from config or region-based default.
// This mimics the Perl _get_first_availability_zone() function.
func (m *Manager) getFirstAvailabilityZone() string {
	return m.getAvailabilityZone(0)
}

func (m *Manager) getBastionSecurityGroup() (string, error) {
	sgName := m.options.BlocName + "-bastion"

	if sg, _ := m.stateManager.GetResource("security_group", sgName); sg != nil {
		return sg.ID, nil
	}

	//nolint:noinlineerr // Idiomatic error checking pattern for optional fallback
	if val, err := m.stateManager.GetOutput("sg_bastion_id"); err == nil {
		if id, ok := val.(string); ok && id != "" {
			return id, nil
		}
	}

	return "", ErrBastionSecurityGroupNotFound(sgName)
}

// getArtifactsSecurityGroup resolves the dedicated artifacts security group
// (<bloc>-artifacts) created by CreateSecurityGroups. It falls back to the
// bastion security group for backward compatibility with blocs bootstrapped
// before the artifacts group existed, so re-running artifacts on an older
// state file still attaches the VM to a reachable group.
func (m *Manager) getArtifactsSecurityGroup() (string, error) {
	sgName := m.options.BlocName + "-artifacts"

	if sg, _ := m.stateManager.GetResource("security_group", sgName); sg != nil {
		return sg.ID, nil
	}

	//nolint:noinlineerr // Idiomatic error checking pattern for optional fallback
	if val, err := m.stateManager.GetOutput("sg_artifacts_id"); err == nil {
		if id, ok := val.(string); ok && id != "" {
			return id, nil
		}
	}

	// Fall back to the bastion SG for pre-artifacts-SG state files.
	return m.getBastionSecurityGroup()
}

// ==============================================================================
// Bastion Instance Creation
// ==============================================================================

func (m *Manager) createBastionInstance(ctx context.Context, bastionName, networkID, subnetID, availabilityZone, sgID string) (*cpi.Instance, error) {
	computeMgr := m.provider.ComputeManager()
	userData := generateBastionUserData(m.config)

	logger.Infof("Creating bastion instance: name=%s flavor=%s image=%s", bastionName, m.config.Bastion.Flavor, m.config.Bastion.Image)

	// Calculate bastion static IP (matches Perl implementation behavior)
	// Get subnet CIDR to calculate IP offset
	bastionIP := ""

	subnetInfo, err := m.getBastionSubnetInfo()
	if err == nil && subnetInfo.CIDR != "" {
		// Default offset is 3 (4th IP: .3 address), matching Perl cidrhost() behavior
		offset := bastionIPSlot

		calculatedIP, err := CalculateIPFromCIDR(subnetInfo.CIDR, offset)
		if err != nil {
			logger.Warnf("Failed to calculate bastion static IP from CIDR %s offset %d: %v", subnetInfo.CIDR, offset, err)
			logger.Warnf("Falling back to DHCP for IP assignment")
		} else {
			bastionIP = calculatedIP
			logger.Infof("Calculated bastion static IP: %s (CIDR: %s, offset: %d)", bastionIP, subnetInfo.CIDR, offset)
		}
	} else {
		logger.Warnf("Could not determine subnet CIDR for bastion, using DHCP for IP assignment")
	}

	// Resolve image and flavor
	imageID, err := m.resolveImageID(ctx, m.config.Bastion.Image)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve image ID for %s: %w", m.config.Bastion.Image, err)
	}

	flavorID, err := m.resolveFlavorID(ctx, m.config.Bastion.Flavor)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve flavor ID for %s: %w", m.config.Bastion.Flavor, err)
	}

	// Check boot volume requirements
	useBootVolume, bootVolumeSize := m.checkBootVolumeRequirements(ctx, computeMgr, flavorID)

	// Adjust subnet for STACKIT virtual networks
	subnetID = m.adjustSubnetForProvider(subnetID)

	// Create instance request with static IP
	req := m.buildInstanceRequest(bastionName, flavorID, imageID, networkID, subnetID, availabilityZone, sgID, userData, useBootVolume, bootVolumeSize)
	req.StaticPrivateIP = bastionIP // Set static IP (empty string means use DHCP)
	req.StaticPrivateIPPrefix = m.bastionStaticIPPrefix()
	// Build the full tailscale spec from defaults + the now-known IP+prefix.
	// PVE provider translates this into SMBIOS for the firstboot script.
	req.Tailscale = m.bastionTailscaleSpec(bastionName, req.StaticPrivateIP, req.StaticPrivateIPPrefix)

	// Tag instance with role for discovery by findBastionIP
	if req.Tags == nil {
		req.Tags = make(map[string]string)
	}

	req.Tags["role"] = "bastion"

	instance, err := computeMgr.CreateInstance(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to create bastion instance %s: %w", bastionName, err)
	}

	return instance, nil
}

//nolint:unparam // Boot volume size may vary in future implementations
func (m *Manager) checkBootVolumeRequirements(ctx context.Context, computeMgr cpi.ComputeManager, flavorID string) (bool, int) {
	const defaultBootVolumeSize = 50

	useBootVolume := false

	if !strings.EqualFold(m.options.Provider, "stackit") {
		return useBootVolume, defaultBootVolumeSize
	}

	flavors, err := computeMgr.ListFlavors(ctx)
	if err != nil {
		return useBootVolume, defaultBootVolumeSize
	}

	for _, flavor := range flavors {
		if flavor.ID == flavorID && flavor.Disk <= 1 {
			useBootVolume = true

			logger.Infof("Detected diskless flavor %s (disk=%dGB), will use boot volume", flavorID, flavor.Disk)

			break
		}
	}

	return useBootVolume, defaultBootVolumeSize
}

func (m *Manager) adjustSubnetForProvider(subnetID string) string {
	// Providers using logical/virtual subnets pass a state-only ID
	// ("virtual:<name>"). The compute API has nothing to attach to, so
	// clear the field and let the provider's CreateInstance fall back to
	// its default network (e.g. the PVE bridge configured at provider init).
	if !strings.HasPrefix(subnetID, "virtual:") {
		return subnetID
	}

	switch strings.ToLower(m.options.Provider) {
	case "stackit", "pve":
		return ""
	}

	return subnetID
}

// getBastionSubnetInfo retrieves the subnet info for bastion IP calculation.
func (m *Manager) getBastionSubnetInfo() (*bastionSubnetInfo, error) {
	// Try to get from current execution context first
	_, subnetInfo, err := m.resolveBastionNetworking()
	if err == nil {
		return subnetInfo, nil
	}

	// Fall back to finding subnet from state
	subnet, err := m.findBastionSubnet()
	if err != nil {
		return nil, err
	}

	return subnet, nil
}

func (m *Manager) buildInstanceRequest(bastionName, flavorID, imageID, networkID, subnetID, availabilityZone, sgID, userData string, useBootVolume bool, bootVolumeSize int) *cpi.InstanceRequest {
	return &cpi.InstanceRequest{
		Name:             bastionName,
		Flavor:           flavorID,
		Image:            imageID,
		KeyPairName:      m.options.BlocName + "-keypair",
		NetworkID:        networkID,
		SubnetID:         subnetID,
		AvailabilityZone: availabilityZone,
		SecurityGroupIDs: []string{sgID},
		UserData:         userData,
		Tags:             m.baseTags(),
		UseBootVolume:    useBootVolume,
		BootVolumeSize:   bootVolumeSize,
		PublicKey:        m.bastionPublicKey(),
		DefaultUsername:  m.bastionDefaultUsername(),
		GatewayIP:        m.bastionGatewayIP(),
		DNSServers:       m.config.Network.DNSServers,
		Hostname:         bastionName,
		DomainSuffix:     m.bastionDomainSuffix(),
		TailscaleAuthKey: m.resolveBastionTailscaleAuthKey(),
	}
}

// resolveBastionTailscaleAuthKey returns the tailscale auth key from the
// merged bloc/global tailscale config. Either a literal AuthKey or a
// vault-path indirection ("path:key") is accepted; bloc>global precedence is
// already applied by mergeTailscaleDefaults at load time. There is no
// fallback to a legacy hard-coded vault path — operators who relied on the
// old behaviour must migrate the value into tailscale.auth_key_vault_path.
//
// Errors are soft: vault unreachable, path missing, or value empty all yield
// "" and a warn-log, so bootstrap completes and the bastion boots without
// tailscale (the operator may join it manually).
func (m *Manager) resolveBastionTailscaleAuthKey() string {
	if m.config == nil || m.config.Tailscale == nil {
		return ""
	}

	ts := m.config.Tailscale

	if key := strings.TrimSpace(ts.AuthKey); key != "" {
		return key
	}

	rawPath := strings.TrimSpace(ts.AuthKeyVaultPath)
	if rawPath == "" {
		return ""
	}

	path, key, ok := splitVaultPathKey(rawPath)
	if !ok {
		logger.Warnf("Tailscale auth key skipped: invalid auth_key_vault_path %q (expected \"path:key\")", rawPath)

		return ""
	}

	safe := m.tailscaleSafe()
	if safe == nil {
		return ""
	}

	val, err := safe.GetString(path, key)
	if err != nil {
		logger.Warnf("Tailscale auth key skipped: %s:%s not readable: %v", path, key, err)

		return ""
	}

	return strings.TrimSpace(val)
}

// tailscaleSafe returns the vault Safe the auth-key resolver should consult.
// Prefers the manager-attached safe (test injection or the artifacts pipeline)
// and falls back to a fresh env-based client. Returns nil with a warn-log when
// no client is available.
func (m *Manager) tailscaleSafe() vault.SafeInterface {
	if m.safe != nil {
		return m.safe
	}

	client, err := vault.NewClientFromEnv()
	if err != nil {
		logger.Warnf("Tailscale auth key skipped: cannot reach vault: %v", err)

		return nil
	}

	return vault.NewSafe(client)
}

// splitVaultPathKey parses the operator-facing "path:key" form into its two
// components. The last colon separates the key, allowing path values that
// contain colons themselves (rare in practice but valid in vault).
func splitVaultPathKey(s string) (path, key string, ok bool) {
	idx := strings.LastIndex(s, ":")
	if idx <= 0 || idx == len(s)-1 {
		return "", "", false
	}

	return s[:idx], s[idx+1:], true
}

// bastionTailscaleSpec returns the full tailscale spec for the bastion,
// resolved from the merged tailscale config plus sensible OCFP defaults
// for fields the operator left unset. Returns nil when no auth key is
// configured so the PVE provider skips SMBIOS injection entirely.
func (m *Manager) bastionTailscaleSpec(hostname, staticIP string, prefix int) *cpi.TailscaleSpec {
	authKey := m.resolveBastionTailscaleAuthKey()
	if authKey == "" {
		return nil
	}

	cfg := tailscaleConfigOrEmpty(m.config)
	spec := &cpi.TailscaleSpec{
		AuthKey:         authKey,
		Hostname:        firstNonEmpty(cfg.Hostname, hostname),
		Tags:            tagsOrDefault(cfg.Tags),
		AcceptDNS:       boolOrDefault(cfg.AcceptDNS, false),
		AcceptRoutes:    boolOrDefault(cfg.AcceptRoutes, false),
		SSH:             boolOrDefault(cfg.SSH, true),
		ExitNode:        cfg.ExitNode,
		AdvertiseRoutes: firstNonEmpty(cfg.AdvertiseRoutes, deriveBastionAdvertiseRoutes(staticIP, prefix)),
	}

	return spec
}

func tailscaleConfigOrEmpty(c *config.Config) *config.TailscaleConfig {
	if c == nil || c.Tailscale == nil {
		return &config.TailscaleConfig{}
	}

	return c.Tailscale
}

func tagsOrDefault(tags []string) []string {
	if len(tags) == 0 {
		return []string{bastionTailscaleDefaultTag}
	}

	out := make([]string, len(tags))
	copy(out, tags)

	return out
}

func boolOrDefault(p *bool, def bool) bool {
	if p == nil {
		return def
	}

	return *p
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}

	return b
}

// deriveBastionAdvertiseRoutes computes the bastion's parent vnet CIDR from
// its static IP + prefix so tailscale can advertise the subnet to the tailnet.
// Returns "" when either input is missing — the firstboot script skips
// --advertise-routes in that case.
func deriveBastionAdvertiseRoutes(staticIP string, prefix int) string {
	addr := strings.TrimSpace(staticIP)
	if addr == "" {
		return ""
	}

	if idx := strings.Index(addr, "/"); idx != -1 {
		addr = addr[:idx]
	}

	if prefix <= 0 || prefix > 32 {
		return ""
	}

	ip := net.ParseIP(addr).To4()
	if ip == nil {
		return ""
	}

	mask := net.CIDRMask(prefix, 32)
	network := ip.Mask(mask)

	return fmt.Sprintf("%s/%d", network.String(), prefix)
}

// bastionDomainSuffix returns the domain suffix that should be appended to the
// bastion hostname to form its FQDN. Sourced from FQDNs.Base when set; empty
// otherwise (the VM then receives an unqualified hostname only).
func (m *Manager) bastionDomainSuffix() string {
	if m.config == nil || m.config.FQDNs == nil {
		return ""
	}

	return strings.TrimSpace(m.config.FQDNs.Base)
}

// bastionPublicKey returns the OpenSSH single-line public key the bastion VM
// should accept for the default user. Providers without a server-side
// keypair primitive (PVE, STACKIT) need the public half passed explicitly
// because nothing else knows the locally-generated value.
//
// Order of preference:
//
//	1. keypair_public_key output written by createKeyPair (canonical source).
//	2. <bloc>/ssh/id_ed25519.pub read from local disk (fallback if state was
//	   wiped but the operator kept the keys).
func (m *Manager) bastionPublicKey() string {
	if val, err := m.stateManager.GetOutput("keypair_public_key"); err == nil {
		if pub, ok := val.(string); ok && strings.TrimSpace(pub) != "" {
			return strings.TrimSpace(pub)
		}
	}

	keyDir := config.OcfpSSHKeyDir(m.options.BlocName)
	pubPath := filepath.Join(keyDir, "id_ed25519.pub")

	data, err := os.ReadFile(pubPath) //nolint:gosec // path components are from trusted config
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(data))
}

// bastionDefaultUsername returns the cloud-init default user the operator
// expects on the bastion. Configurable via the bloc's bastion.user field so
// each provider's image default ("ubuntu", "debian", "ec2-user", ...) can
// be honoured without hardcoding.
func (m *Manager) bastionDefaultUsername() string {
	if m.config.Bastion.SSHUser != "" {
		return m.config.Bastion.SSHUser
	}

	return ""
}

// bastionStaticIPPrefix returns the subnet prefix length the bastion VM
// should advertise as "local" — the parent vnet/network CIDR's prefix, not
// the per-AZ subnet ocfp carves out for accounting. PVE SDN simple zones
// present a single L3 subnet per vnet (e.g. 10.64.64.0/18); the gateway lives
// in that parent network. Pairing the bastion IP with a narrower mask (e.g.
// /20 from an AZ subnet, or the legacy /24 default) puts the gateway off-link
// and breaks egress. Zero means "let the provider default decide."
func (m *Manager) bastionStaticIPPrefix() int {
	if m.config == nil {
		return 0
	}

	for _, cidr := range []string{m.config.Network.CIDR, m.config.Network.NetworkCIDR} {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}

		if _, ipnet, err := net.ParseCIDR(cidr); err == nil && ipnet != nil {
			ones, _ := ipnet.Mask.Size()
			if ones > 0 && ones <= 32 {
				return ones
			}
		}
	}

	return 0
}

// bastionGatewayIP returns the explicit default-gateway IP for the bastion's
// subnet when one was recorded by CreateSubnets — required for PVE static
// addressing (cloud-init ipconfig0 needs the gw= component to set a default
// route on the bastion's NIC).
func (m *Manager) bastionGatewayIP() string {
	subnetInfo, err := m.getBastionSubnetInfo()
	if err != nil || subnetInfo == nil {
		return ""
	}

	if gw, err := m.stateManager.GetOutput(fmt.Sprintf("subnet_%s_gateway", subnetInfo.Name)); err == nil {
		if s, ok := gw.(string); ok && s != "" {
			return s
		}
	}

	if subnetInfo.CIDR != "" {
		return CIDRGatewayIP(subnetInfo.CIDR)
	}

	return ""
}

func (m *Manager) attachBastionPublicIP(ctx context.Context, instanceID, bastionName string) error {
	netMgr := m.provider.NetworkManager()
	if netMgr == nil {
		return errNetworkManagerNotAvailable
	}

	// Try to find bastion public IP - first in state, then in cloud provider
	bastionIPName := bastionName

	bastionIP, err := m.stateManager.GetResource("public_ip", bastionIPName)
	if err != nil || bastionIP == nil {
		// Not in state - query cloud provider for public IP with job=bastion and index=0
		logger.Debugf("Bastion public IP not in state, checking cloud provider")

		publicIPs, err := netMgr.ListPublicIPs(ctx)
		if err != nil {
			return fmt.Errorf("failed to list public IPs: %w", err)
		}

		// Find public IP with labels job="bastion" and index="0"
		var foundIP *cpi.PublicIP

		for _, ip := range publicIPs {
			if ip.Labels != nil && ip.Labels["job"] == "bastion" && ip.Labels["index"] == "0" {
				foundIP = ip
				logger.Infof("Found bastion public IP in cloud provider: id=%s, address=%s", ip.ID, ip.Address)

				break
			}
		}

		if foundIP == nil {
			return errBastionPublicIPNotFound
		}

		// Use the found public IP
		bastionIP = &state.Resource{
			ID: foundIP.ID,
			Properties: map[string]interface{}{
				"ip_address": foundIP.Address,
			},
		}
	} else {
		logger.Infof("Found bastion public IP in state: id=%s", bastionIP.ID)
	}

	publicIPID := bastionIP.ID
	logger.Infof("Attaching public IP %s to bastion instance %s", publicIPID, instanceID)

	err = netMgr.AssociateFloatingIP(ctx, publicIPID, instanceID)
	if err != nil {
		return fmt.Errorf("failed to associate public IP: %w", err)
	}

	logger.Infof("Successfully attached public IP to bastion")

	// Update the bastion state with the public IP address
	if ipAddr, ok := bastionIP.Properties["ip_address"].(string); ok {
		_ = m.stateManager.SetOutput("bastion_public_ip", ipAddr)
		logger.Infof("Bastion public IP: %s", ipAddr)
	}

	return nil
}

// ==============================================================================
// Image Resolution
// ==============================================================================

func (m *Manager) resolveImageID(ctx context.Context, imageNameOrID string) (string, error) {
	if strings.TrimSpace(imageNameOrID) == "" {
		return "", nil
	}

	// Check if already an ID
	if id, isID := m.checkIfImageID(imageNameOrID); isID {
		return id, nil
	}

	// Look up image by name
	return m.lookupImageByName(ctx, imageNameOrID)
}

func (m *Manager) checkIfImageID(imageNameOrID string) (string, bool) {
	// If it's already an AMI ID (starts with ami-), return as-is
	if strings.HasPrefix(imageNameOrID, "ami-") {
		return imageNameOrID, true
	}

	// If it's already a UUID (36 characters), return as-is
	if len(imageNameOrID) == 36 && !strings.Contains(imageNameOrID, " ") {
		return imageNameOrID, true
	}

	return "", false
}

func (m *Manager) lookupImageByName(ctx context.Context, imageNameOrID string) (string, error) {
	computeMgr := m.provider.ComputeManager()
	filters := m.buildImageFilters(imageNameOrID)

	images, err := computeMgr.ListImages(ctx, filters)
	if err != nil {
		return "", fmt.Errorf("failed to list images: %w", err)
	}

	// Try pattern match first (for AMI wildcards)
	if id, found := m.tryPatternMatch(filters, images, imageNameOrID); found {
		return id, nil
	}

	// Try exact and partial matches
	if id, found := m.tryNameMatches(images, imageNameOrID); found {
		return id, nil
	}

	// Auto-provision: if the missing name is a known PVE catalog template,
	// build it on the cluster and return the new VMID. Operators get a
	// recoverable lab instead of "image not found."
	if id, ok := m.tryAutoProvisionPVETemplate(ctx, imageNameOrID); ok {
		return id, nil
	}

	// Log debug info and return error
	m.logImageSearchResults(images, imageNameOrID)

	return "", errImageNotFoundWithName(imageNameOrID)
}

// tryAutoProvisionPVETemplate dispatches to the PVE provider's template
// provisioner when (a) the bloc targets PVE and (b) the missing name is in
// the OCFP-shipped template catalog. Returns the VMID as a decimal string
// (the form resolveImageID's caller expects).
func (m *Manager) tryAutoProvisionPVETemplate(ctx context.Context, name string) (string, bool) {
	if !shouldAutoProvisionTemplate(m.options.Provider, name) {
		return "", false
	}

	pveCompute, ok := m.provider.ComputeManager().(*pveclient.ComputeManager)
	if !ok {
		return "", false
	}

	logger.Infof("pve: template %q absent, auto-provisioning", name)

	vmid, err := pveCompute.ProvisionTemplate(ctx, name)
	if err != nil {
		logger.Errorf("pve: template auto-provision failed for %q: %v", name, err)

		return "", false
	}

	return strconv.Itoa(vmid), true
}

// shouldAutoProvisionTemplate decides whether the auto-provision path applies
// for the given provider + image name. Pure function for testability.
func shouldAutoProvisionTemplate(provider, name string) bool {
	if !strings.EqualFold(provider, "pve") {
		return false
	}

	_, known := pveclient.LookupCatalogSpec(name)

	return known
}

func (m *Manager) tryPatternMatch(filters map[string]string, images []*cpi.Image, imageNameOrID string) (string, bool) {
	nameFilter, hasNameFilter := filters["name"]
	if hasNameFilter && strings.Contains(nameFilter, "*") && len(images) > 0 {
		logger.Infof("Resolved image '%s' to '%s' (ID: %s)", imageNameOrID, images[0].Name, images[0].ID)

		return images[0].ID, true
	}

	return "", false
}

func (m *Manager) tryNameMatches(images []*cpi.Image, imageNameOrID string) (string, bool) {
	// Try exact match first
	for _, img := range images {
		if strings.EqualFold(img.Name, imageNameOrID) {
			logger.Infof("Resolved image '%s' to ID: %s", imageNameOrID, img.ID)

			return img.ID, true
		}
	}

	// Try partial match (case-insensitive)
	imageNameLower := strings.ToLower(imageNameOrID)
	for _, img := range images {
		if strings.Contains(strings.ToLower(img.Name), imageNameLower) {
			logger.Infof("Resolved image '%s' to '%s' (ID: %s)", imageNameOrID, img.Name, img.ID)

			return img.ID, true
		}
	}

	return "", false
}

func (m *Manager) logImageSearchResults(images []*cpi.Image, imageNameOrID string) {
	if len(images) == 0 {
		return
	}

	logger.Infof("No match for '%s'. First few images found:", imageNameOrID)

	for i, img := range images {
		if i >= maxDebugImagesDisplay {
			break
		}

		logger.Infof("  - %s (ID: %s)", img.Name, img.ID)
	}
}

func (m *Manager) buildImageFilters(imageNameOrID string) map[string]string {
	filters := make(map[string]string)

	// AWS-specific filters for Ubuntu images
	if strings.EqualFold(m.options.Provider, "aws") {
		imageNameLower := strings.ToLower(imageNameOrID)

		// Check if looking for Ubuntu
		if strings.Contains(imageNameLower, "ubuntu") {
			// Canonical's AWS account ID for Ubuntu AMIs
			filters["owner"] = "099720109477"
			filters["architecture"] = "x86_64"
			filters["root-device-type"] = "ebs"
			filters["virtualization-type"] = "hvm"

			// Build name pattern based on version (Ubuntu 24.04+ uses gp3, older use gp2)
			switch {
			case strings.Contains(imageNameLower, "24.04"):
				filters["name"] = "ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*"
			case strings.Contains(imageNameLower, "22.04"):
				filters["name"] = "ubuntu/images/hvm-ssd-gp3/ubuntu-jammy-22.04-amd64-server-*"
			case strings.Contains(imageNameLower, "20.04"):
				filters["name"] = "ubuntu/images/hvm-ssd/ubuntu-focal-20.04-amd64-server-*"
			}
		}
	}

	return filters
}

// ==============================================================================
// Flavor Resolution
// ==============================================================================

// logStackitFlavors logs available flavors for STACKIT debugging.
func (m *Manager) logStackitFlavors(flavors []*cpi.Flavor, flavorNameOrID string) {
	if !strings.EqualFold(m.options.Provider, "stackit") {
		return
	}

	logger.Infof("STACKIT: Looking for flavor '%s', provider=%s", flavorNameOrID, m.options.Provider)
	logger.Infof("Available flavors count: %d", len(flavors))

	for _, flavor := range flavors {
		logger.Infof("  - Flavor: ID='%s', Name='%s', Disk=%dGB, VCPUs=%d, RAM=%dMB",
			flavor.ID, flavor.Name, flavor.Disk, flavor.VCPUs, flavor.RAM)
	}
}

// findMatchingFlavor looks for a flavor matching the given name or ID.
func (m *Manager) findMatchingFlavor(flavors []*cpi.Flavor, flavorNameOrID string, minDiskGB int) (string, bool) {
	for _, flavor := range flavors {
		if strings.EqualFold(m.options.Provider, "stackit") {
			logger.Debugf("Comparing flavor: requested='%s' vs ID='%s' or Name='%s'",
				flavorNameOrID, flavor.ID, flavor.Name)
		}

		if flavor.ID == flavorNameOrID || strings.EqualFold(flavor.Name, flavorNameOrID) {
			logger.Infof("Found matching flavor: ID='%s', Name='%s', Disk=%dGB", flavor.ID, flavor.Name, flavor.Disk)

			// For STACKIT and AWS, accept diskless flavors (they use boot volumes/EBS)
			// STACKIT API reports disk=1 for diskless flavors, AWS reports disk=0
			isDiskless := (strings.EqualFold(m.options.Provider, "stackit") && flavor.Disk <= 1) ||
				(strings.EqualFold(m.options.Provider, "aws") && flavor.Disk == 0)
			if isDiskless {
				logger.Infof("Using diskless flavor '%s' (will use boot volume/EBS)", flavor.ID)

				return flavor.ID, true
			}

			// For other providers or flavors with disk, check minimum size
			if flavor.Disk >= minDiskGB {
				logger.Infof("Using flavor '%s' with %d GB disk", flavor.ID, flavor.Disk)

				return flavor.ID, true
			}

			logger.Warnf("Specified flavor '%s' has insufficient disk (%d GB < %d GB required)",
				flavorNameOrID, flavor.Disk, minDiskGB)

			return "", false
		}
	}

	return "", false
}

// selectSmallestSuitableFlavor selects the smallest flavor meeting requirements.
func (m *Manager) selectSmallestSuitableFlavor(flavors []*cpi.Flavor, minDiskGB int) *cpi.Flavor {
	var selectedFlavor *cpi.Flavor

	isStackit := strings.EqualFold(m.options.Provider, "stackit")
	isAWS := strings.EqualFold(m.options.Provider, "aws")

	for _, flavor := range flavors {
		suitable := false
		// STACKIT and AWS both support diskless flavors that use EBS/boot volumes
		// STACKIT API reports disk=1 for diskless flavors, AWS reports disk=0
		isDiskless := (isStackit && flavor.Disk <= 1) || (isAWS && flavor.Disk == 0)
		if isDiskless {
			suitable = true
		} else if flavor.Disk >= minDiskGB {
			suitable = true
		}

		if suitable {
			if selectedFlavor == nil || flavor.VCPUs < selectedFlavor.VCPUs ||
				(flavor.VCPUs == selectedFlavor.VCPUs && flavor.RAM < selectedFlavor.RAM) {
				selectedFlavor = flavor
			}
		}
	}

	return selectedFlavor
}

func (m *Manager) resolveFlavorID(ctx context.Context, flavorNameOrID string) (string, error) {
	if strings.TrimSpace(flavorNameOrID) == "" {
		return "", nil
	}

	const minDiskGB = 3 // Minimum disk size in GB for Ubuntu images

	computeMgr := m.provider.ComputeManager()

	// For AWS and STACKIT, try GetFlavor first
	if id, found := m.tryGetFlavorDirect(ctx, computeMgr, flavorNameOrID, minDiskGB); found {
		return id, nil
	}

	// Fall back to listing and searching
	return m.searchFlavorInList(ctx, computeMgr, flavorNameOrID, minDiskGB)
}

func (m *Manager) tryGetFlavorDirect(ctx context.Context, computeMgr cpi.ComputeManager, flavorNameOrID string, minDiskGB int) (string, bool) {
	if !strings.EqualFold(m.options.Provider, "aws") && !strings.EqualFold(m.options.Provider, "stackit") {
		return "", false
	}

	flavor, err := computeMgr.GetFlavor(ctx, flavorNameOrID)
	if err != nil || flavor == nil {
		return "", false
	}

	// Check if suitable
	isDiskless := (strings.EqualFold(m.options.Provider, "stackit") && flavor.Disk <= 1) ||
		(strings.EqualFold(m.options.Provider, "aws") && flavor.Disk == 0)

	if isDiskless || flavor.Disk >= minDiskGB {
		logger.Infof("Using requested flavor: %s", flavorNameOrID)

		return flavorNameOrID, true
	}

	logger.Warnf("Flavor '%s' has insufficient disk (%d GB < %d GB required)", flavorNameOrID, flavor.Disk, minDiskGB)

	return "", false
}

func (m *Manager) searchFlavorInList(ctx context.Context, computeMgr cpi.ComputeManager, flavorNameOrID string, minDiskGB int) (string, error) {
	flavors, err := computeMgr.ListFlavors(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list flavors: %w", err)
	}

	m.logStackitFlavors(flavors, flavorNameOrID)
	logger.Infof("Resolving flavor '%s' (total flavors: %d)", flavorNameOrID, len(flavors))

	// Check if the specified flavor exists
	if flavorID, found := m.findMatchingFlavor(flavors, flavorNameOrID, minDiskGB); found {
		logger.Infof("Using requested flavor: %s", flavorID)

		return flavorID, nil
	}

	logger.Warnf("Requested flavor '%s' not found or unsuitable, selecting smallest suitable flavor", flavorNameOrID)

	return m.selectFallbackFlavor(flavors, minDiskGB)
}

func (m *Manager) selectFallbackFlavor(flavors []*cpi.Flavor, minDiskGB int) (string, error) {
	selectedFlavor := m.selectSmallestSuitableFlavor(flavors, minDiskGB)
	if selectedFlavor == nil {
		if strings.EqualFold(m.options.Provider, "stackit") {
			return "", errNoSuitableFlavorWithDisk(minDiskGB)
		}

		return "", errNoFlavorWithDiskSize(minDiskGB)
	}

	m.logSelectedFlavor(selectedFlavor)

	return selectedFlavor.ID, nil
}

func (m *Manager) logSelectedFlavor(flavor *cpi.Flavor) {
	if flavor.Disk == 0 {
		logger.Infof("Selected STACKIT diskless flavor '%s' (vCPUs: %d, RAM: %d MB) - will use boot volume",
			flavor.ID, flavor.VCPUs, flavor.RAM)
	} else {
		logger.Infof("Selected flavor '%s' (vCPUs: %d, RAM: %d MB, Disk: %d GB) to meet disk requirements",
			flavor.ID, flavor.VCPUs, flavor.RAM, flavor.Disk)
	}
}

// ==============================================================================
// Key Pair Management
// ==============================================================================

func (m *Manager) createKeyPair(ctx context.Context) error {
	keypairName := m.options.BlocName + "-keypair"

	// Check if keypair already exists in state
	existingKeypair, _ := m.stateManager.GetResource(state.ResourceTypeKeyPair, keypairName)
	if existingKeypair != nil {
		shouldSkip, err := m.verifyExistingKeypair(ctx, keypairName)
		if err != nil {
			return err
		}

		if shouldSkip {
			return nil
		}
	}

	return m.createNewKeyPair(ctx, keypairName)
}

// verifyExistingKeypair verifies if an existing keypair is valid in state, cloud, and locally.
// Returns (shouldSkip=true, nil) if keypair is fully valid and creation should be skipped.
// Returns (shouldSkip=false, nil) if keypair needs to be recreated.
// Returns (shouldSkip=false, err) if verification failed with an error.
func (m *Manager) verifyExistingKeypair(ctx context.Context, keypairName string) (bool, error) {
	// Check if local key file exists
	keyDir := config.OcfpSSHKeyDir(m.options.BlocName)
	keyFile := filepath.Join(keyDir, "id_ed25519")
	localKeyExists := false

	_, err := os.Stat(keyFile) //nolint:gosec // path components are from trusted config
	if err == nil {
		localKeyExists = true
	}

	// Verify keypair exists in cloud provider
	computeMgr := m.provider.ComputeManager()
	cloudKeypair, err := computeMgr.GetKeyPair(ctx, keypairName)

	if err == nil && cloudKeypair != nil {
		// Keypair exists in both state and cloud
		if localKeyExists {
			// Perfect: state, cloud, and local file all exist
			_, _ = fmt.Fprintf(os.Stdout, "    • SSH keypair %s already exists, skipping\n", keypairName)
			logger.Infof("Keypair %s already exists in state, cloud, and local file - skipping creation", keypairName)

			return true, nil
		}

		// Cloud keypair exists but local file missing - can't recover private key
		return false, m.handleOrphanedCloudKeypair(ctx, keypairName, keyFile, computeMgr)
	}

	// Keypair in state but NOT in cloud - state is stale
	m.handleStaleKeypairState(keypairName)

	return false, nil
}

// handleOrphanedCloudKeypair handles the case where a keypair exists in cloud but not locally.
func (m *Manager) handleOrphanedCloudKeypair(ctx context.Context, keypairName, keyFile string, computeMgr cpi.ComputeManager) error {
	logger.Warnf("Keypair %s exists in cloud but local private key not found at %s", keypairName, keyFile)
	_, _ = fmt.Fprintf(os.Stdout, "    • SSH keypair %s exists in cloud but private key missing locally\n", keypairName)
	_, _ = fmt.Fprintf(os.Stdout, "    • Cannot retrieve private key from cloud - deleting and recreating\n")

	// Delete from cloud and recreate
	deleteErr := computeMgr.DeleteKeyPair(ctx, keypairName)
	if deleteErr != nil {
		return fmt.Errorf("failed to delete orphaned keypair from cloud: %w", deleteErr)
	}

	// Remove from state
	_ = m.stateManager.RemoveResource(state.ResourceTypeKeyPair, keypairName)

	return nil
}

// handleStaleKeypairState handles the case where a keypair exists in state but not in cloud.
func (m *Manager) handleStaleKeypairState(keypairName string) {
	logger.Warnf("Keypair %s in state but not found in cloud - removing stale state entry", keypairName)
	_, _ = fmt.Fprintf(os.Stdout, "    • SSH keypair %s in state but not in cloud, recreating\n", keypairName)

	// Remove from state
	_ = m.stateManager.RemoveResource(state.ResourceTypeKeyPair, keypairName)
}

// createLocalKeyPair handles keypair creation for providers that lack a
// server-side keypair-mint primitive (STACKIT, PVE). The private half is
// generated locally and only the public half is uploaded; for PVE the public
// key is later injected at VM-create time via cloud-init.
// Returns (keypair, shouldSavePrivateKey, error).
// shouldSavePrivateKey is true when keys were newly generated, false when read from existing files.
func (m *Manager) createLocalKeyPair(ctx context.Context, computeMgr cpi.ComputeManager, keypairName string) (*cpi.KeyPair, bool, error) {
	// Generate SSH key pair locally (or read from existing files)
	privateKeyData, publicKeyData, wasReadFromFile, err := m.generateLocalSSHKeyPair()
	if err != nil {
		return nil, false, err
	}

	publicKeyStr := strings.TrimSpace(string(publicKeyData))

	// DEBUG: Log first 50 bytes of privateKeyData for verification
	logger.Debugf("generateLocalSSHKeyPair returned privateKeyData (first 50 bytes): %s...", string(privateKeyData[:min(50, len(privateKeyData))])) //nolint:mnd
	logger.Debugf("generateLocalSSHKeyPair returned publicKeyStr: %s", publicKeyStr)
	logger.Debugf("Keys were read from existing file: %v", wasReadFromFile)

	// Check if keypair already exists at the provider
	existingKey, err := m.checkExistingLocalKeyPair(ctx, computeMgr, keypairName, publicKeyStr, privateKeyData)
	if err == nil && existingKey != nil {
		// Keypair exists and matches - don't save private key (already on disk)
		return existingKey, false, nil
	}

	// Import the public key to the provider
	err = m.importLocalPublicKey(ctx, computeMgr, keypairName, publicKeyStr)
	if err != nil {
		return nil, false, err
	}

	// Create a KeyPair object for consistency
	keypair := &cpi.KeyPair{
		ID:         keypairName, // STACKIT/PVE use name as ID
		Name:       keypairName,
		PublicKey:  publicKeyStr,
		PrivateKey: string(privateKeyData),
	}

	// DEBUG: Log keypair.PrivateKey that will be saved
	logger.Debugf("keypair.PrivateKey (first 50 bytes): %s...", keypair.PrivateKey[:min(50, len(keypair.PrivateKey))]) //nolint:mnd

	// Return the inverse of wasReadFromFile:
	// - If read from existing file (wasReadFromFile=true), DON'T save again (return false)
	// - If newly generated (wasReadFromFile=false), MUST save (return true)
	shouldSavePrivKey := !wasReadFromFile
	logger.Debugf("shouldSavePrivKey=%v (wasReadFromFile=%v)", shouldSavePrivKey, wasReadFromFile)

	return keypair, shouldSavePrivKey, nil
}

// generateLocalSSHKeyPair generates an Ed25519 SSH key pair and returns the private and public key data.
// Returns (privateKeyData, publicKeyData, wasReadFromExistingFile, error).
// wasReadFromExistingFile is true when keys were read from existing local files, false when newly generated.
func (m *Manager) generateLocalSSHKeyPair() ([]byte, []byte, bool, error) {
	// Check if local keypair already exists
	keyDir := config.OcfpSSHKeyDir(m.options.BlocName)
	existingKeyPath := filepath.Join(keyDir, "id_ed25519")
	existingPubPath := filepath.Join(keyDir, "id_ed25519.pub")

	// If local keypair exists, reuse it
	_, keyStatErr := os.Stat(existingKeyPath) //nolint:gosec // path components are from trusted config
	if keyStatErr == nil {                    //nolint:nestif // SSH key validation requires nested checks
		_, pubStatErr := os.Stat(existingPubPath) //nolint:gosec // path components are from trusted config
		if pubStatErr == nil {
			_, _ = fmt.Fprintf(os.Stdout, "      ↳ Using existing local ed25519 key pair...\n")

			privateKeyData, readErr := os.ReadFile(existingKeyPath) //nolint:gosec // path is controlled
			if readErr != nil {
				return nil, nil, false, fmt.Errorf("failed to read existing private key: %w", readErr)
			}

			publicKeyData, readErr := os.ReadFile(existingPubPath) //nolint:gosec // path is controlled
			if readErr != nil {
				return nil, nil, false, fmt.Errorf("failed to read existing public key: %w", readErr)
			}

			// Return true to indicate keys were read from existing files - DON'T save them again!
			return privateKeyData, publicKeyData, true, nil
		}
	}

	// No existing keypair - generate a new one
	keyManager := ssh.NewKeyManager()
	tempKeyPath := filepath.Join(os.TempDir(), fmt.Sprintf("ocfp-temp-key-%d", time.Now().UnixNano()))

	defer func() {
		_ = os.Remove(tempKeyPath)
		_ = os.Remove(tempKeyPath + ".pub")
	}()

	// Generate Ed25519 key pair (more secure and efficient than RSA)
	_, _ = fmt.Fprintf(os.Stdout, "      ↳ Generating ed25519 key pair locally...\n")

	err := keyManager.GenerateKeyPair(tempKeyPath, "ed25519", 0)
	if err != nil {
		return nil, nil, false, fmt.Errorf("failed to generate SSH keypair: %w", err)
	}

	// Read the generated keys
	privateKeyPath := filepath.Clean(tempKeyPath)

	privateKeyData, err := os.ReadFile(privateKeyPath) // #nosec G304 -- path is generated internally
	if err != nil {
		return nil, nil, false, fmt.Errorf("failed to read private key: %w", err)
	}

	publicKeyPath := filepath.Clean(tempKeyPath + ".pub")

	publicKeyData, err := os.ReadFile(publicKeyPath) // #nosec G304 -- path is generated internally
	if err != nil {
		return nil, nil, false, fmt.Errorf("failed to read public key: %w", err)
	}

	// Return false to indicate keys were newly generated - MUST save them!
	return privateKeyData, publicKeyData, false, nil
}

// checkExistingLocalKeyPair checks if a keypair already exists at the provider
// and returns it if found. If the remote keypair doesn't match the local public
// key, it deletes the remote keypair and returns nil to force regeneration and
// re-upload, ensuring bootstrap always works with synchronized keys.
func (m *Manager) checkExistingLocalKeyPair(ctx context.Context, computeMgr cpi.ComputeManager, keypairName, publicKeyStr string, privateKeyData []byte) (*cpi.KeyPair, error) {
	providerLabel := m.providerDisplayName()

	existingKey, getErr := computeMgr.GetKeyPair(ctx, keypairName)
	if getErr == nil && existingKey != nil {
		// Verify that the cloud public key matches our local public key
		localPubKey := strings.TrimSpace(publicKeyStr)
		cloudPubKey := strings.TrimSpace(existingKey.PublicKey)

		if localPubKey != cloudPubKey {
			// Keys don't match - delete cloud keypair and force regeneration
			logger.Warnf("Keypair %s exists at %s but public key doesn't match local key", keypairName, providerLabel)

			_, _ = fmt.Fprintf(os.Stdout, "      ↳ Keypair exists at %s but doesn't match local key\n", providerLabel)
			_, _ = fmt.Fprintf(os.Stdout, "      ↳ Deleting remote keypair and re-uploading local public key\n")

			// Delete the mismatched keypair from the provider
			deleteErr := computeMgr.DeleteKeyPair(ctx, keypairName)
			if deleteErr != nil {
				return nil, fmt.Errorf("failed to delete mismatched keypair from %s: %w", providerLabel, deleteErr)
			}

			logger.Infof("Deleted mismatched keypair %s from %s, will re-upload", keypairName, providerLabel)

			// Return nil to trigger re-upload of the local public key
			return nil, nil
		}

		// Keys match - reuse existing keypair
		logger.Infof("Keypair %s already exists at %s and matches local key, skipping import", keypairName, providerLabel)

		_, _ = fmt.Fprintf(os.Stdout, "      ↳ Keypair already exists at %s and matches local key, skipping upload\n", providerLabel)

		// Create a KeyPair object for consistency
		return &cpi.KeyPair{
			ID:         keypairName,
			Name:       keypairName,
			PublicKey:  publicKeyStr,
			PrivateKey: string(privateKeyData),
		}, nil
	}

	if getErr != nil {
		return nil, fmt.Errorf("failed to check existing keypair: %w", getErr)
	}

	return nil, nil
}

// importLocalPublicKey imports a locally-generated public key to the provider,
// handling conflicts gracefully.
func (m *Manager) importLocalPublicKey(ctx context.Context, computeMgr cpi.ComputeManager, keypairName, publicKeyStr string) error {
	providerLabel := m.providerDisplayName()

	_, _ = fmt.Fprintf(os.Stdout, "      ↳ Uploading public key to %s...\n", providerLabel)

	err := computeMgr.ImportKeyPair(ctx, keypairName, publicKeyStr)
	if err != nil {
		// Check if it's a conflict error (already exists)
		if strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "already exists") {
			logger.Infof("Keypair %s already exists at %s (conflict), continuing", keypairName, providerLabel)

			_, _ = fmt.Fprintf(os.Stdout, "      ↳ Keypair already exists at %s (continuing)\n", providerLabel)

			return nil
		}

		return fmt.Errorf("failed to import keypair to %s: %w", providerLabel, err)
	}

	_, _ = fmt.Fprintf(os.Stdout, "      ↳ Public key imported to %s successfully\n", providerLabel)

	return nil
}

// providerDisplayName returns a human-readable label for the current provider,
// used in operator-facing log/console output. Falls back to "provider" when no
// provider is configured.
func (m *Manager) providerDisplayName() string {
	switch strings.ToLower(m.options.Provider) {
	case "":
		return "provider"
	case "pve":
		return "PVE"
	case "aws":
		return "AWS"
	case "gcp":
		return "GCP"
	case "stackit":
		return "STACKIT"
	default:
		return m.options.Provider
	}
}

// saveKeyPairToState saves the keypair to state and sets outputs.
func (m *Manager) saveKeyPairToState(keypair *cpi.KeyPair, keypairName string) error {
	err := m.stateManager.AddResource(&state.Resource{
		ID:       keypair.ID,
		Type:     state.ResourceTypeKeyPair,
		Name:     keypairName,
		Provider: m.options.Provider,
		State:    string(cpi.ResourceStateActive),
		Properties: map[string]interface{}{
			"public_key":  keypair.PublicKey,
			"fingerprint": keypair.Fingerprint,
		},
		Tags:      m.baseTags(),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	if err != nil {
		return fmt.Errorf("failed to save keypair to state: %w", err)
	}

	// Set outputs
	_ = m.stateManager.SetOutput("keypair_name", keypairName)
	_ = m.stateManager.SetOutput("keypair_id", keypair.ID)
	_ = m.stateManager.SetOutput("keypair_public_key", keypair.PublicKey)
	_ = m.stateManager.SetOutput("keypair_fingerprint", keypair.Fingerprint)

	return nil
}

// handleDuplicateKeyPair handles the case where a keypair already exists in AWS.
// If we have the local keypair, we reuse it. Otherwise, we delete and recreate.
// Returns the keypair and a boolean indicating whether the private key should be saved.
func (m *Manager) handleDuplicateKeyPair(ctx context.Context, computeMgr cpi.ComputeManager, keypairName string) (*cpi.KeyPair, bool, error) {
	keyDir := config.OcfpSSHKeyDir(m.options.BlocName)
	keyFile := filepath.Join(keyDir, "id_ed25519")

	// Check if we have the local keypair (Ed25519 preferred)
	//nolint:noinlineerr // Idiomatic file existence check pattern
	if _, err := os.Stat(keyFile); err == nil { //nolint:gosec // path components are from trusted config
		// Local keypair exists - fetch from AWS and reuse
		_, _ = fmt.Fprintf(os.Stdout, "    • SSH keypair %s already exists in AWS and local key found, reusing\n", keypairName)
		logger.Infof("Keypair %s already exists in AWS and local key found at %s, reusing", keypairName, keyFile)

		keypair, err := computeMgr.GetKeyPair(ctx, keypairName)
		if err != nil {
			return nil, false, fmt.Errorf("failed to get existing keypair from AWS: %w", err)
		}

		// Return false to skip saving private key (it already exists locally)
		return keypair, false, nil
	}

	// Check for RSA key fallback (pre-existing deployments)
	rsaKeyFile := filepath.Join(keyDir, "id_rsa")

	//nolint:noinlineerr // Idiomatic file existence check pattern
	if _, err := os.Stat(rsaKeyFile); err == nil { //nolint:gosec // path components are from trusted config
		// Local RSA keypair exists - fetch from AWS and reuse
		_, _ = fmt.Fprintf(os.Stdout, "    • SSH keypair %s already exists in AWS and local RSA key found, reusing\n", keypairName)
		logger.Infof("Keypair %s already exists in AWS and local RSA key found at %s, reusing", keypairName, rsaKeyFile)

		keypair, err := computeMgr.GetKeyPair(ctx, keypairName)
		if err != nil {
			return nil, false, fmt.Errorf("failed to get existing keypair from AWS: %w", err)
		}

		// Return false to skip saving private key (it already exists locally)
		return keypair, false, nil
	}

	// Local keypair doesn't exist - delete from AWS and recreate
	_, _ = fmt.Fprintf(os.Stdout, "    • SSH keypair %s exists in AWS but no local key found, deleting and recreating\n", keypairName)
	logger.Infof("Keypair %s exists in AWS but no local key found, deleting and recreating", keypairName)

	err := computeMgr.DeleteKeyPair(ctx, keypairName)
	if err != nil {
		return nil, false, fmt.Errorf("failed to delete existing keypair from AWS: %w", err)
	}

	// Recreate the keypair
	keypair, err := computeMgr.CreateKeyPair(ctx, &cpi.KeyPairRequest{
		Name:    keypairName,
		KeyType: "ed25519",
		Tags:    m.baseTags(),
	})
	if err != nil {
		return nil, false, fmt.Errorf("failed to recreate keypair: %w", err)
	}

	// Return true to save the new private key
	return keypair, true, nil
}

func (m *Manager) createNewKeyPair(ctx context.Context, keypairName string) error {
	_, _ = fmt.Fprintf(os.Stdout, "    • Creating SSH keypair %s (ed25519)...\n", keypairName)
	logger.Infof("Creating keypair: name=%s", keypairName)

	keypair, shouldSavePrivKey, err := m.createKeypairWithProvider(ctx, keypairName)
	if err != nil {
		return err
	}

	if shouldSavePrivKey {
		err = m.savePrivateKeyAndConfig(keypair.PrivateKey, keypairName)
		if err != nil {
			return err
		}
	}

	err = m.saveKeyPairToState(keypair, keypairName)
	if err != nil {
		return err
	}

	logger.Infof("Keypair created successfully: id=%s", keypair.ID)

	return nil
}

// createKeypairWithProvider creates a keypair using the appropriate provider logic.
func (m *Manager) createKeypairWithProvider(ctx context.Context, keypairName string) (*cpi.KeyPair, bool, error) {
	computeMgr := m.provider.ComputeManager()

	// STACKIT and PVE both lack a server-side keypair primitive — the
	// provider has no CreateKeyPair API; keys are generated locally and the
	// public half is injected at VM-create time (STACKIT via the keypair
	// service, PVE via cloud-init). Route both through the local-gen path.
	if m.providerUsesLocalKeypairs() {
		return m.createLocalKeyPair(ctx, computeMgr, keypairName)
	}

	return m.createStandardKeyPair(ctx, computeMgr, keypairName)
}

// providerUsesLocalKeypairs reports whether the current provider expects
// keypairs to be generated locally and have only the public half imported
// (rather than calling the provider's CreateKeyPair to mint a new pair).
func (m *Manager) providerUsesLocalKeypairs() bool {
	switch strings.ToLower(m.options.Provider) {
	case "stackit", "pve":
		return true
	}

	return false
}

// createStandardKeyPair creates a keypair for non-STACKIT providers.
func (m *Manager) createStandardKeyPair(ctx context.Context, computeMgr cpi.ComputeManager, keypairName string) (*cpi.KeyPair, bool, error) {
	keypair, err := computeMgr.CreateKeyPair(ctx, &cpi.KeyPairRequest{
		Name:    keypairName,
		KeyType: "ed25519",
		Tags:    m.baseTags(),
	})
	if err == nil {
		return keypair, true, nil
	}

	if !strings.Contains(err.Error(), "InvalidKeyPair.Duplicate") {
		return nil, false, fmt.Errorf("failed to create keypair: %w", err)
	}

	keypair, shouldSavePrivKey, err := m.handleDuplicateKeyPair(ctx, computeMgr, keypairName)
	if err != nil {
		return nil, false, fmt.Errorf("failed to handle duplicate keypair: %w", err)
	}

	return keypair, shouldSavePrivKey, nil
}

// savePrivateKeyAndConfig saves the private key to file and config.
func (m *Manager) savePrivateKeyAndConfig(privateKey, keypairName string) error {
	err := m.savePrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("failed to save private key: %w", err)
	}

	if m.config.Keys == nil {
		m.config.Keys = make(map[string]string)
	}

	m.config.Keys[keypairName] = privateKey

	err = config.SaveBlocKeys(m.options.BlocName, m.config.Keys)
	if err != nil {
		logger.Warnf("Failed to save SSH key to state: %v", err)
	} else {
		logger.Infof("Saved SSH key to state file for portability")
	}

	return nil
}

func (m *Manager) savePrivateKey(privateKey string) error {
	if strings.TrimSpace(privateKey) == "" {
		return errEmptyPrivateKey
	}

	keyDir := config.OcfpSSHKeyDir(m.options.BlocName)
	keyFile := filepath.Join(keyDir, "id_ed25519")

	// DEBUG: Log what we're about to save
	logger.Debugf("savePrivateKey called with data (first 50 bytes): %s...", privateKey[:min(50, len(privateKey))]) //nolint:mnd

	// Create directory if it doesn't exist
	err := os.MkdirAll(keyDir, sshKeyDirMode) //nolint:gosec // path components are from trusted config
	if err != nil {
		return fmt.Errorf("failed to create SSH key directory: %w", err)
	}

	// Write private key
	err = os.WriteFile(keyFile, []byte(privateKey), sshKeyFileMode) //nolint:gosec // path components are from trusted config
	if err != nil {
		return fmt.Errorf("failed to write private key: %w", err)
	}

	logger.Infof("Private key saved to: %s", keyFile)

	_, _ = fmt.Fprintf(os.Stdout, "      ↳ Private key saved to: ~/.ocfp/%s/ssh/id_ed25519\n", m.options.BlocName)

	return nil
}

// ==============================================================================
// Bastion State Management
// ==============================================================================

func (m *Manager) saveBastionToState(instance *cpi.Instance, bastionName string) error {
	err := m.stateManager.AddResource(&state.Resource{
		ID:       instance.ID,
		Type:     state.ResourceTypeInstance,
		Name:     bastionName,
		Provider: m.options.Provider,
		State:    string(cpi.ResourceStateActive),
		Properties: map[string]interface{}{
			"flavor":          instance.Flavor,
			"image":           instance.Image,
			"private_ip":      instance.PrivateIP,
			"public_ip":       instance.PublicIP,
			"keypair":         instance.KeyPair,
			"subnet_id":       instance.SubnetID,
			"security_groups": instance.SecurityGroups,
		},
		Tags:      m.baseTags(),
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})
	if err != nil {
		return fmt.Errorf("failed to save bastion to state: %w", err)
	}

	// Add dependencies
	subnetName := m.getDefaultSubnetName()
	m.addBastionDependencies(bastionName, subnetName)

	// Save outputs
	m.saveBastionOutputs(instance)

	return nil
}

func (m *Manager) addBastionDependencies(bastionName, subnetName string) {
	_ = m.stateManager.AddDependency("instance."+bastionName, "subnet."+subnetName)
	_ = m.stateManager.AddDependency("instance."+bastionName, "security_group."+m.options.BlocName+"-bastion")
	_ = m.stateManager.AddDependency("instance."+bastionName, "keypair."+m.options.BlocName+"-keypair")
}

func (m *Manager) getDefaultSubnetName() string {
	return m.options.BlocName + "-ocfp-0"
}

func (m *Manager) saveBastionOutputs(instance *cpi.Instance) {
	_ = m.stateManager.SetOutput("bastion_id", instance.ID)
	_ = m.stateManager.SetOutput("bastion_private_ip", instance.PrivateIP)

	if instance.PublicIP != "" {
		_ = m.stateManager.SetOutput("bastion_public_ip", instance.PublicIP)
	}

	// bastion_ssh_host is the address `ocfp ssh bastion` should connect to.
	// Providers like PVE in bridge mode have no public IP — the bastion is
	// reachable on its private LAN/Tailscale address, and that becomes the
	// SSH target. For providers with public IPs (AWS, STACKIT), keep using
	// the public address so external operators can connect.
	sshHost := bastionSSHHost(instance)
	if sshHost != "" {
		_ = m.stateManager.SetOutput("bastion_ssh_host", sshHost)
	}

	_ = m.stateManager.SetOutput("bastion_flavor", instance.Flavor)
	_ = m.stateManager.SetOutput("bastion_image", instance.Image)

	// SSH connection info
	if instance.PublicIP != "" {
		// Determine which key file to use (prefer ed25519, fallback to rsa)
		keyFile := "id_ed25519"

		keyDir := config.OcfpSSHKeyDir(m.options.BlocName)

		//nolint:noinlineerr // Idiomatic file existence check for key type fallback
		if _, err := os.Stat(filepath.Join(keyDir, "id_ed25519")); err != nil { //nolint:gosec // path components are from trusted config
			// Check for RSA key fallback
			if _, err := os.Stat(filepath.Join(keyDir, "id_rsa")); err == nil { //nolint:gosec // path components are from trusted config
				keyFile = "id_rsa"
			}
		}

		sshCommand := fmt.Sprintf("ssh -i ~/.ocfp/%s/ssh/%s ubuntu@%s", m.options.BlocName, keyFile, instance.PublicIP)
		_ = m.stateManager.SetOutput("bastion_ssh_command", sshCommand)
	}
}

// ==============================================================================
// Utility Functions
// ==============================================================================

// bastionSSHHost picks the right address for `ocfp ssh bastion` to dial.
// Public/floating addresses win when available so external operators can
// connect from anywhere; otherwise the private address is used (PVE bridge
// mode, on-prem deployments reachable via VPN/Tailscale).
func bastionSSHHost(instance *cpi.Instance) string {
	for _, candidate := range []string{instance.PublicIP, instance.FloatingIP, instance.PrivateIP} {
		if candidate != "" {
			return candidate
		}
	}

	return ""
}

func generateBastionUserData(_cfg *config.Config) string {
	return `#!/bin/bash
# Bootstrap bastion instance
apt-get update
apt-get install -y build-essential zlibc zlib1g-dev ruby ruby-dev openssl libxslt1-dev libxml2-dev libssl-dev libreadline-dev libyaml-dev libsqlite3-dev sqlite3
`
}

// ==============================================================================
// Display Functions
// ==============================================================================
