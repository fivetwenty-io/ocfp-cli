package cpi

import "time"

// Network represents a VPC or virtual network
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

// Subnet represents a network subnet
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

// Instance represents a compute instance
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
	KeyPair          string
	AvailabilityZone string
	Tags             map[string]string
	Volumes          []string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Volume represents a block storage volume
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

// Snapshot represents a volume snapshot
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

// SecurityGroup represents a security group
type SecurityGroup struct {
	ID          string
	Name        string
	Description string
	NetworkID   string
	Rules       []*SecurityRule
	Tags        map[string]string
	CreatedAt   time.Time
}

// SecurityRule represents a security group rule
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

// FloatingIP represents a floating/elastic IP
type FloatingIP struct {
	ID         string
	Address    string
	Status     string // available, associated
	InstanceID string
	NetworkID  string
	Tags       map[string]string
	CreatedAt  time.Time
}

// PublicIP represents a public IP address
type PublicIP struct {
	ID         string
	Address    string
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

// Router represents a network router
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

// Route represents a network route
type Route struct {
	Destination string
	NextHop     string
}

// LoadBalancer represents a load balancer
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

// Backend represents a load balancer backend
type Backend struct {
	ID      string
	Name    string
	Address string
	Port    int
	Weight  int
	Enabled bool
	Health  string // healthy, unhealthy, unknown
}

// HealthCheck represents a health check configuration
type HealthCheck struct {
	Protocol           string // http, https, tcp
	Port               int
	Path               string
	Interval           int // seconds
	Timeout            int // seconds
	HealthyThreshold   int
	UnhealthyThreshold int
}

// BackendPool represents a load balancer backend pool
type BackendPool struct {
	ID      string
	Name    string
	Members []*BackendMember
}

// BackendMember represents a backend pool member
type BackendMember struct {
	ID         string
	IPAddress  string
	Port       int
	TargetPort int
	Weight     int
	Status     string // active, draining, disabled
}

// HealthStatus represents the health status of a load balancer
type HealthStatus struct {
	LoadBalancerID string
	Healthy        int
	Unhealthy      int
	Total          int
	Backends       map[string]string // backend ID -> health status
}

// KeyPair represents an SSH key pair
type KeyPair struct {
	Name        string
	Fingerprint string
	PublicKey   string
	PrivateKey  string // Only populated on creation
	CreatedAt   time.Time
}

// Image represents a machine image
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

// Flavor represents an instance type/flavor
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

// Bucket represents an object storage bucket
type Bucket struct {
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

// CreateNetworkRequest for creating a network
type CreateNetworkRequest struct {
	Name       string
	CIDR       string
	DNSServers []string
	Tags       map[string]string
}

// CreateSubnetRequest for creating a subnet
type CreateSubnetRequest struct {
	Name             string
	NetworkID        string
	CIDR             string
	AvailabilityZone string
	Type             string // public, private
	Tags             map[string]string
}

// CreateInstanceRequest for creating an instance
type CreateInstanceRequest struct {
	Name             string
	Flavor           string
	Image            string
	NetworkID        string
	SubnetID         string
	SecurityGroups   []string
	KeyPair          string
	UserData         string
	AvailabilityZone string
	Tags             map[string]string
}

// CreateVolumeRequest for creating a volume
type CreateVolumeRequest struct {
	Name             string
	Size             int // GB
	Type             string
	Encrypted        bool
	SnapshotID       string // Source snapshot ID for creating volume from snapshot
	SourceSnapshot   string // Deprecated: Use SnapshotID instead
	AvailabilityZone string
	Tags             map[string]string
}

// CreateSnapshotRequest for creating a snapshot
type CreateSnapshotRequest struct {
	Name        string
	VolumeID    string
	Description string
	Tags        map[string]string
}

// CreateSecurityGroupRequest for creating a security group
type CreateSecurityGroupRequest struct {
	Name        string
	Description string
	NetworkID   string
	Rules       []*SecurityRule
	Tags        map[string]string
}

// AllocateFloatingIPRequest for allocating a floating IP
type AllocateFloatingIPRequest struct {
	NetworkID string
	Tags      map[string]string
}

// CreatePublicIPRequest for creating a public IP
type CreatePublicIPRequest struct {
	Name      string
	Job       string // router, cf-ssh, jumpbox, tcp-router, ops
	Index     string // 0-based index
	NetworkID string
	Labels    map[string]string
	Tags      map[string]string
}

// CreateRouterRequest for creating a router
type CreateRouterRequest struct {
	Name            string
	NetworkID       string
	ExternalGateway string
	Tags            map[string]string
}

// CreateLoadBalancerRequest for creating a load balancer
type CreateLoadBalancerRequest struct {
	Name           string
	Type           string // application, network
	Scheme         string // internet-facing, internal
	NetworkID      string
	SubnetIDs      []string
	SecurityGroups []string
	Tags           map[string]string
}

// UpdateLoadBalancerRequest for updating a load balancer
type UpdateLoadBalancerRequest struct {
	Name           *string
	SecurityGroups []string
	Tags           map[string]string
}
