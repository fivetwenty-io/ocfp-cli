package gcp

import (
	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/storage"
)

// NetworkManager handles network-related operations for GCP.
type NetworkManager struct {
	client *Client
}

// ComputeManager handles compute-related operations for GCP.
type ComputeManager struct {
	client *Client
}

// StorageManager handles storage-related operations for GCP.
type StorageManager struct {
	client *Client
}

// SecurityManager handles security-related operations for GCP.
type SecurityManager struct {
	client *Client
}

// LoadBalancerManager handles load balancer operations for GCP.
type LoadBalancerManager struct {
	client *Client
}

// getInstancesClient returns the instances client.
func (c *Client) getInstancesClient() *compute.InstancesClient {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.instancesClient
}

// getDisksClient returns the disks client.
func (c *Client) getDisksClient() *compute.DisksClient {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.disksClient
}

// getSnapshotsClient returns the snapshots client.
func (c *Client) getSnapshotsClient() *compute.SnapshotsClient {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.snapshotsClient
}

// getImagesClient returns the images client.
func (c *Client) getImagesClient() *compute.ImagesClient {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.imagesClient
}

// getMachineTypesClient returns the machine types client.
func (c *Client) getMachineTypesClient() *compute.MachineTypesClient {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.machineTypesClient
}

// getNetworksClient returns the networks client.
func (c *Client) getNetworksClient() *compute.NetworksClient {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.networksClient
}

// getSubnetworksClient returns the subnetworks client.
func (c *Client) getSubnetworksClient() *compute.SubnetworksClient {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.subnetworksClient
}

// getFirewallsClient returns the firewalls client.
func (c *Client) getFirewallsClient() *compute.FirewallsClient {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.firewallsClient
}

// getAddressesClient returns the addresses client.
func (c *Client) getAddressesClient() *compute.AddressesClient {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.addressesClient
}

// getRoutersClient returns the routers client.
func (c *Client) getRoutersClient() *compute.RoutersClient {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.routersClient
}

// getStorageClient returns the storage client.
func (c *Client) getStorageClient() *storage.Client {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.storageClient
}

// getForwardingRulesClient returns the forwarding rules client.
func (c *Client) getForwardingRulesClient() *compute.ForwardingRulesClient {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.forwardingRulesClient
}

// getBackendServicesClient returns the backend services client.
func (c *Client) getBackendServicesClient() *compute.BackendServicesClient {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.backendServicesClient
}

// getHealthChecksClient returns the health checks client.
func (c *Client) getHealthChecksClient() *compute.HealthChecksClient {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.healthChecksClient
}

