package cpi

import "time"

// Network represents a VPC or virtual network.
type Network struct {
	ID         string
	Name       string
	CIDR       string
	Region     string
	State      ResourceState
	Tags       map[string]string
	DNSServers []string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Subnet represents a network subnet.
type Subnet struct {
	ID               string
	Name             string
	NetworkID        string
	CIDR             string
	AvailabilityZone string
	Type             string // public, private
	State            ResourceState
	Tags             map[string]string
	CreatedAt        time.Time
}

// Instance represents a compute instance.
type Instance struct {
	ID               string
	Name             string
	State            ResourceState
	Flavor           string
	Image            string
	NetworkID        string
	SubnetID         string
	PrivateIP        string
	PublicIP         string
	FloatingIP       string
	SecurityGroups   []string
	SecurityGroupIDs []string // Alternative field name used in bootstrap code
	KeyPair          string
	KeyPairName      string // Alternative field name used in bootstrap code
	AvailabilityZone string
	Tags             map[string]string
	Volumes          []string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Volume represents a block storage volume.
type Volume struct {
	ID         string
	Name       string
	Size       int // GB
	Type       string
	State      ResourceState
	Encrypted  bool
	AttachedTo string
	Device     string
	Tags       map[string]string
	CreatedAt  time.Time
}

// Snapshot represents a volume snapshot.
type Snapshot struct {
	ID          string
	Name        string
	VolumeID    string
	Size        int // GB
	State       ResourceState
	Description string
	Tags        map[string]string
	CreatedAt   time.Time
}

// SecurityGroup represents a security group.
type SecurityGroup struct {
	ID          string
	Name        string
	Description string
	NetworkID   string
	Rules       []*SecurityRule
	Tags        map[string]string
	CreatedAt   time.Time
}

// SecurityRule represents a security group rule.
type SecurityRule struct {
	ID           string
	Direction    string // ingress, egress
	Protocol     string // tcp, udp, icmp, all
	PortRangeMin int
	PortRangeMax int
	RemoteIPCIDR string
	RemoteGroup  string
	Description  string
}

// FloatingIP represents a floating/elastic IP.
type FloatingIP struct {
	ID         string
	Address    string
	Status     string // available, associated
	InstanceID string
	NetworkID  string
	Tags       map[string]string
	CreatedAt  time.Time
}

// PublicIP represents a public IP address.
type PublicIP struct {
	ID         string
	IPAddress  string // Use IPAddress for compatibility with bootstrap code
	Address    string // Keep Address for other compatibility
	Name       string
	Status     string // available, associated, pending
	Job        string // router, cf-ssh, jumpbox, tcp-router, ops
	Index      string // 0-based index for the IP
	InstanceID string
	NetworkID  string
	Labels     map[string]string
	Tags       map[string]string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// NetworkInterface represents a network interface card (NIC).
type NetworkInterface struct {
	ID               string
	Name             string
	IPv4             string
	IPv6             string
	MAC              string
	NetworkID        string
	InstanceID       string
	SecurityGroupIDs []string
	AllowedAddresses []string
	Labels           map[string]string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Router represents a network router.
type Router struct {
	ID              string
	Name            string
	NetworkID       string
	ExternalGateway string
	State           ResourceState
	Routes          []*Route
	Interfaces      []string // subnet IDs
	Tags            map[string]string
	CreatedAt       time.Time
}

// Route represents a network route.
type Route struct {
	Destination string
	NextHop     string
}

// LoadBalancer represents a load balancer.
type LoadBalancer struct {
	ID             string
	Name           string
	Type           string // external, internal
	Algorithm      string // round-robin, least-connections, ip-hash
	IPAddress      string // Load balancer IP address
	Port           int
	TargetPort     int
	Protocol       string // tcp, http, https
	Status         string // active, pending, error
	State          ResourceState
	NetworkID      string
	SubnetIDs      []string
	SecurityGroups []string
	Backends       []*Backend
	HealthCheck    *HealthCheck
	Tags           []string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Backend represents a load balancer backend.
type Backend struct {
	ID      string
	Name    string
	Address string
	Port    int
	Weight  int
	Enabled bool
	Health  string // healthy, unhealthy, unknown
}

// HealthCheck represents a health check configuration.
type HealthCheck struct {
	Protocol           string // http, https, tcp
	Port               int
	Path               string
	Interval           int // seconds
	Timeout            int // seconds
	HealthyThreshold   int
	UnhealthyThreshold int
}

// BackendPool represents a load balancer backend pool.
type BackendPool struct {
	ID      string
	Name    string
	Members []*BackendMember
}

// BackendMember represents a backend pool member.
type BackendMember struct {
	ID         string
	IPAddress  string
	Port       int
	TargetPort int
	Weight     int
	Status     string // active, draining, disabled
}

// HealthStatus represents the health status of a load balancer.
type HealthStatus struct {
	LoadBalancerID string
	Healthy        int
	Unhealthy      int
	Total          int
	Backends       map[string]string // backend ID -> health status
}

// KeyPair represents an SSH key pair.
type KeyPair struct {
	ID          string
	Name        string
	Fingerprint string
	PublicKey   string
	PrivateKey  string //nolint:gosec // field name is descriptive, not a hardcoded secret
	CreatedAt   time.Time
}

// Image represents a machine image.
type Image struct {
	ID           string
	Name         string
	Description  string
	OS           string
	OSVersion    string
	Architecture string
	Size         int64 // bytes
	MinDisk      int   // GB
	MinRAM       int   // MB
	Public       bool
	State        string
	Tags         map[string]string
	CreatedAt    time.Time
}

// Flavor represents an instance type/flavor.
type Flavor struct {
	ID          string
	Name        string
	VCPUs       int
	RAM         int // MB
	Disk        int // GB
	Ephemeral   int // GB
	NetworkCap  int // Mbps
	Description string
}

// Bucket represents an object storage bucket.
type Bucket struct {
	ID           string
	Name         string
	Region       string
	StorageClass string
	Versioning   bool
	Encryption   bool
	Public       bool
	Size         int64 // bytes
	ObjectCount  int64
	Tags         map[string]string
	CreatedAt    time.Time
}

// Request types for resource creation

// CreateSecurityGroupRequest for creating a security group.
type CreateSecurityGroupRequest struct {
	Name        string
	Description string
	NetworkID   string
	Rules       []*SecurityRule
	Tags        map[string]string
}

// AllocateFloatingIPRequest for allocating a floating IP.
type AllocateFloatingIPRequest struct {
	NetworkID string
	Tags      map[string]string
}

// PublicIPRequest represents a request for creating/managing public IPs.
type PublicIPRequest struct {
	Name      string
	Job       string // router, cf-ssh, jumpbox, tcp-router, ops
	Index     string // 0-based index
	NetworkID string
	Labels    map[string]string
	Tags      map[string]string
}

// NetworkRequest represents a request for creating/managing networks.
type NetworkRequest struct {
	Name        string
	CIDR        string
	DNSServers  []string
	Description string
	Tags        map[string]string
}

// SubnetRequest represents a request for creating subnets.
type SubnetRequest struct {
	Name             string
	NetworkID        string
	CIDR             string
	AvailabilityZone string
	Type             string // public, private
	Tags             map[string]string
	// Gateway is the subnet's gateway IP. For providers whose subnets are real
	// L3 segments (e.g. PVE SDN), this becomes the segment's routed gateway and
	// must lie within CIDR. Empty leaves the provider default.
	Gateway string
	// SNAT enables source NAT on the subnet's gateway (PVE SDN). Ignored by
	// providers that manage egress separately.
	SNAT bool
}

// SecurityGroupRequest represents a request for creating security groups.
type SecurityGroupRequest struct {
	Name        string
	Description string
	NetworkID   string
	Rules       []*SecurityRule
	Tags        map[string]string
}

// KeyPairRequest represents a request for creating key pairs.
type KeyPairRequest struct {
	Name    string
	KeyType string // Key type: ed25519 (default) or rsa
	Tags    map[string]string
}

// VolumeRequest represents a request for creating volumes.
type VolumeRequest struct {
	Name             string
	Size             int // Legacy field name
	SizeGB           int
	VolumeType       string
	Type             string // Legacy field name
	AvailabilityZone string
	Encrypted        bool
	Tags             map[string]string
	// InstanceID is the VM id that will own the volume. Required by providers
	// where volumes cannot be unowned (PVE local-lvm/local-zfs reject vmid=0);
	// ignored by providers whose volumes exist independently of an instance.
	InstanceID string
}

// InstanceRequest represents a request for creating instances.
type InstanceRequest struct {
	Name                  string
	Flavor                string
	Image                 string
	KeyPair               string // Legacy field name
	KeyPairName           string
	NetworkID             string
	SubnetID              string
	SecurityGroups        []string // Legacy field name
	SecurityGroupIDs      []string
	UserData              string
	AvailabilityZone      string
	Tags                  map[string]string
	BootVolumeSize        int            // Size in GB for boot volume (for diskless flavors)
	UseBootVolume         bool           // Use boot volume instead of direct image (for STACKIT diskless flavors)
	StaticPrivateIP       string         // Optional: specific private IP to assign (STACKIT-specific, matches Perl implementation)
	StaticPrivateIPPrefix int            // Optional: subnet prefix length (e.g. 18) to pair with StaticPrivateIP when the address itself lacks /N. Used by PVE to write the correct mask in cloud-init ipconfig0 when the L3 subnet (e.g. an SDN vnet /18) is larger than the logical AZ subnet ocfp carves from it. Zero = leave to provider default.
	TailscaleAuthKey      string         // DEPRECATED: use Tailscale instead. Retained for callers not yet migrated; ignored when Tailscale is non-nil.
	Tailscale             *TailscaleSpec // Optional: full tailscale config. When non-nil + AuthKey set, the PVE provider injects via SMBIOS for the bastion firstboot/watchdog to read.
	// Cloudflare, when non-nil, provisions a cloudflared connector on the
	// bastion via SMBIOS alongside tailscale.
	Cloudflare      *CloudflareSpec
	PublicKey       string   // Optional: SSH public key (OpenSSH single-line form) to inject at VM-create time (PVE cloud-init sshkeys)
	DefaultUsername string   // Optional: cloud-init default username (PVE ciuser); defaults to image's built-in user when empty
	GatewayIP       string   // Optional: explicit default gateway for static IP configurations (PVE bridge mode)
	DNSServers      []string // Optional: DNS resolvers to push via cloud-init (PVE nameserver)
	// Hostname is the short host name the VM should adopt at first boot.
	// Used to drive the user-data snippet's `hostname:` and `fqdn:` keys so
	// the cloned VM stops booting as the template default (e.g. "ubuntu-22045").
	Hostname string
	// DomainSuffix combines with Hostname to produce the FQDN. Typically the
	// bloc's FQDNs.Base. Empty falls back to just Hostname.
	DomainSuffix string
	// VCPUsOverride, when > 0, replaces the flavor preset's vCPU count for
	// this request only. Zero leaves the preset value. Honored by providers
	// that read the flavor at create time (currently PVE).
	VCPUsOverride int
	// MemoryMiBOverride, when > 0, replaces the flavor preset's RAM (in MiB)
	// for this request only. Zero leaves the preset value. Honored by
	// providers that read the flavor at create time (currently PVE).
	MemoryMiBOverride int
}

// TailscaleSpec describes how a VM should join the tailnet at first boot.
// Bootstrap resolves it from the bloc/provider tailscale: config + the vault
// auth-key path, then sets it on InstanceRequest.Tailscale. The PVE provider
// translates it into an SMBIOS payload the firstboot script reads.
type TailscaleSpec struct {
	// AuthKey is the tailscale auth key (resolved from vault). Required.
	// Should be reusable + preauthorized so the watchdog can re-up after
	// a tailnet drop without operator intervention.
	AuthKey string

	// Hostname is the tailnet hostname (also used for the bastion's
	// /etc/hostname). Defaults to InstanceRequest.Name when empty.
	Hostname string

	// Tags is the list of tailscale ACL tags (e.g. ["tag:ocfp-bastion"]).
	// At least one tag is recommended so ACLs can target OCFP bastions.
	Tags []string

	// AcceptDNS, when true, lets tailscaled rewrite /etc/resolv.conf.
	// Default false: avoids "MagicDNS unreachable on tailnet drop breaks
	// reconnect" failure mode from commit 3a2efab.
	AcceptDNS bool

	// AcceptRoutes, when true, lets the node import other peers' advertised
	// subnet routes. Default false on bastions: prevents loops where the
	// bastion's own /18 returns via tailscale0 instead of the local bridge.
	AcceptRoutes bool

	// SSH, when true, enables tailscale-ssh on the node so operators can
	// SSH via the tailnet. Default true.
	SSH bool

	// ExitNode, when non-empty, configures this node to route all egress
	// through the named tailnet exit node. Empty disables.
	ExitNode string

	// AdvertiseRoutes is the subnet CIDR this node advertises (e.g.
	// "10.64.64.0/18"). Bootstrap derives from StaticPrivateIP+prefix.
	// Empty skips --advertise-routes.
	AdvertiseRoutes string
}

// CloudflareSpec is the bastion-side cloudflared connector config delivered
// via SMBIOS. Only the connector token is needed for a remotely-managed
// tunnel; ingress lives in Cloudflare.
type CloudflareSpec struct {
	// TunnelToken is the connector token from EnsureTunnel. Empty disables
	// cloudflared provisioning on the bastion.
	TunnelToken string
}

// BucketRequest represents a request for creating buckets.
type BucketRequest struct {
	Name string
	Tags map[string]string
}

// CredentialsGroupRequest represents a request for creating credentials groups.
type CredentialsGroupRequest struct {
	Name string
	Tags map[string]string
}

// CredentialsGroup represents a credentials group.
type CredentialsGroup struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

// CreateRouterRequest for creating a router.
type CreateRouterRequest struct {
	Name            string
	NetworkID       string
	ExternalGateway string
	Tags            map[string]string
}

// CreateLoadBalancerRequest for creating a load balancer.
type CreateLoadBalancerRequest struct {
	Name           string
	Type           string // application, network
	Scheme         string // internet-facing, internal
	NetworkID      string
	SubnetIDs      []string
	SecurityGroups []string
	Tags           map[string]string
}

// UpdateLoadBalancerRequest for updating a load balancer.
type UpdateLoadBalancerRequest struct {
	Name           *string
	SecurityGroups []string
	Tags           map[string]string
}
