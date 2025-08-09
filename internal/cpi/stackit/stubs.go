package stackit

import (
	"context"
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// ComputeManager stub implementation
type ComputeManager struct {
	client *Client
}

func (m *ComputeManager) CreateInstance(ctx context.Context, req *cpi.CreateInstanceRequest) (*cpi.Instance, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *ComputeManager) GetInstance(ctx context.Context, id string) (*cpi.Instance, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *ComputeManager) ListInstances(ctx context.Context, filters map[string]string) ([]*cpi.Instance, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *ComputeManager) StartInstance(ctx context.Context, id string) error {
	return fmt.Errorf("not implemented")
}

func (m *ComputeManager) StopInstance(ctx context.Context, id string) error {
	return fmt.Errorf("not implemented")
}

func (m *ComputeManager) RebootInstance(ctx context.Context, id string) error {
	return fmt.Errorf("not implemented")
}

func (m *ComputeManager) DeleteInstance(ctx context.Context, id string) error {
	return fmt.Errorf("not implemented")
}

func (m *ComputeManager) CreateKeyPair(ctx context.Context, name string) (*cpi.KeyPair, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *ComputeManager) ImportKeyPair(ctx context.Context, name string, publicKey string) error {
	return fmt.Errorf("not implemented")
}

func (m *ComputeManager) GetKeyPair(ctx context.Context, name string) (*cpi.KeyPair, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *ComputeManager) ListKeyPairs(ctx context.Context) ([]*cpi.KeyPair, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *ComputeManager) DeleteKeyPair(ctx context.Context, name string) error {
	return fmt.Errorf("not implemented")
}

func (m *ComputeManager) ListImages(ctx context.Context, filters map[string]string) ([]*cpi.Image, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *ComputeManager) GetImage(ctx context.Context, id string) (*cpi.Image, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *ComputeManager) ListFlavors(ctx context.Context) ([]*cpi.Flavor, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *ComputeManager) GetFlavor(ctx context.Context, id string) (*cpi.Flavor, error) {
	return nil, fmt.Errorf("not implemented")
}

// StorageManager stub implementation
type StorageManager struct {
	client *Client
}

func (m *StorageManager) CreateVolume(ctx context.Context, req *cpi.CreateVolumeRequest) (*cpi.Volume, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *StorageManager) GetVolume(ctx context.Context, id string) (*cpi.Volume, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *StorageManager) ListVolumes(ctx context.Context, filters map[string]string) ([]*cpi.Volume, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *StorageManager) AttachVolume(ctx context.Context, volumeID string, instanceID string, device string) error {
	return fmt.Errorf("not implemented")
}

func (m *StorageManager) DetachVolume(ctx context.Context, volumeID string) error {
	return fmt.Errorf("not implemented")
}

func (m *StorageManager) ResizeVolume(ctx context.Context, id string, size int) error {
	return fmt.Errorf("not implemented")
}

func (m *StorageManager) DeleteVolume(ctx context.Context, id string) error {
	return fmt.Errorf("not implemented")
}

func (m *StorageManager) CreateSnapshot(ctx context.Context, volumeID string, name string) (*cpi.Snapshot, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *StorageManager) GetSnapshot(ctx context.Context, id string) (*cpi.Snapshot, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *StorageManager) ListSnapshots(ctx context.Context, volumeID string) ([]*cpi.Snapshot, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *StorageManager) DeleteSnapshot(ctx context.Context, id string) error {
	return fmt.Errorf("not implemented")
}

func (m *StorageManager) CreateBucket(ctx context.Context, name string) (*cpi.Bucket, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *StorageManager) GetBucket(ctx context.Context, name string) (*cpi.Bucket, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *StorageManager) ListBuckets(ctx context.Context) ([]*cpi.Bucket, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *StorageManager) DeleteBucket(ctx context.Context, name string) error {
	return fmt.Errorf("not implemented")
}

func (m *StorageManager) EmptyBucket(ctx context.Context, name string) error {
	return fmt.Errorf("not implemented")
}

// SecurityManager stub implementation
type SecurityManager struct {
	client *Client
}

func (m *SecurityManager) CreateSecurityGroup(ctx context.Context, req *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *SecurityManager) GetSecurityGroup(ctx context.Context, id string) (*cpi.SecurityGroup, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *SecurityManager) ListSecurityGroups(ctx context.Context, filters map[string]string) ([]*cpi.SecurityGroup, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *SecurityManager) DeleteSecurityGroup(ctx context.Context, id string) error {
	return fmt.Errorf("not implemented")
}

func (m *SecurityManager) AddSecurityRule(ctx context.Context, groupID string, rule *cpi.SecurityRule) error {
	return fmt.Errorf("not implemented")
}

func (m *SecurityManager) RemoveSecurityRule(ctx context.Context, groupID string, ruleID string) error {
	return fmt.Errorf("not implemented")
}

func (m *SecurityManager) ListSecurityRules(ctx context.Context, groupID string) ([]*cpi.SecurityRule, error) {
	return nil, fmt.Errorf("not implemented")
}

// LoadBalancerManager stub implementation
type LoadBalancerManager struct {
	client *Client
}

func (m *LoadBalancerManager) CreateLoadBalancer(ctx context.Context, req *cpi.CreateLoadBalancerRequest) (*cpi.LoadBalancer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *LoadBalancerManager) GetLoadBalancer(ctx context.Context, id string) (*cpi.LoadBalancer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *LoadBalancerManager) ListLoadBalancers(ctx context.Context, filters map[string]string) ([]*cpi.LoadBalancer, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *LoadBalancerManager) UpdateLoadBalancer(ctx context.Context, id string, req *cpi.UpdateLoadBalancerRequest) error {
	return fmt.Errorf("not implemented")
}

func (m *LoadBalancerManager) DeleteLoadBalancer(ctx context.Context, id string) error {
	return fmt.Errorf("not implemented")
}

func (m *LoadBalancerManager) AddBackend(ctx context.Context, lbID string, backend *cpi.Backend) error {
	return fmt.Errorf("not implemented")
}

func (m *LoadBalancerManager) RemoveBackend(ctx context.Context, lbID string, backendID string) error {
	return fmt.Errorf("not implemented")
}

func (m *LoadBalancerManager) EnableBackend(ctx context.Context, lbID string, backendID string) error {
	return fmt.Errorf("not implemented")
}

func (m *LoadBalancerManager) DisableBackend(ctx context.Context, lbID string, backendID string) error {
	return fmt.Errorf("not implemented")
}

func (m *LoadBalancerManager) ConfigureHealthCheck(ctx context.Context, lbID string, check *cpi.HealthCheck) error {
	return fmt.Errorf("not implemented")
}

func (m *LoadBalancerManager) GetHealthStatus(ctx context.Context, lbID string) (*cpi.HealthStatus, error) {
	return nil, fmt.Errorf("not implemented")
}