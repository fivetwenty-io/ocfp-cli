package azure

// NetworkManager handles network-related operations for Azure.
type NetworkManager struct {
	client *Client
}

// ComputeManager handles compute-related operations for Azure.
type ComputeManager struct {
	client *Client
}

// StorageManager handles storage-related operations for Azure.
type StorageManager struct {
	client *Client
}

// SecurityManager handles security-related operations for Azure.
type SecurityManager struct {
	client *Client
}

// LoadBalancerManager handles load balancer operations for Azure.
type LoadBalancerManager struct {
	client *Client
}
