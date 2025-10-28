//go:build integration

package aws

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// Integration test configuration
const (
	testTimeout    = 5 * time.Minute
	testVPCCIDR    = "10.100.0.0/16"
	testSubnetCIDR = "10.100.1.0/24"
	testRegion     = "us-west-2"
	testAZ         = "us-west-2a"
	testNamePrefix = "ocfp-inttest"
)

// setupTestClient creates a test client for integration testing.
// It checks for AWS credentials and skips if not available.
func setupTestClient(t *testing.T) (*Client, func()) {
	t.Helper()

	// Check for AWS credentials
	if os.Getenv("AWS_ACCESS_KEY_ID") == "" && os.Getenv("AWS_PROFILE") == "" {
		t.Skip("Skipping integration test: AWS credentials not found (set AWS_ACCESS_KEY_ID or AWS_PROFILE)")
	}

	// Check for LocalStack endpoint override
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		endpoint = os.Getenv("LOCALSTACK_ENDPOINT")
	}

	config := &Config{
		Region:      testRegion,
		EndpointURL: endpoint,
	}

	client, err := NewClient(config)
	if err != nil {
		t.Fatalf("Failed to create test client: %v", err)
	}

	// Initialize client
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := client.Initialize(ctx, config); err != nil {
		t.Fatalf("Failed to initialize test client: %v", err)
	}

	// Validate credentials
	if err := client.ValidateCredentials(ctx); err != nil {
		t.Skipf("Skipping integration test: AWS credentials invalid: %v", err)
	}

	// Return cleanup function
	cleanup := func() {
		ctx := context.Background()
		_ = client.Cleanup(ctx)
	}

	return client, cleanup
}

// generateTestName creates a unique test resource name
func generateTestName(prefix string) string {
	return fmt.Sprintf("%s-%s-%d", testNamePrefix, prefix, time.Now().Unix())
}

// TestIntegration_VPCLifecycle tests the full VPC lifecycle
func TestIntegration_VPCLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client, cleanup := setupTestClient(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	nm := client.NetworkManager()

	// Create VPC
	vpcName := generateTestName("vpc")
	t.Logf("Creating VPC: %s", vpcName)

	vpc, err := nm.CreateNetwork(ctx, &cpi.NetworkRequest{
		Name: vpcName,
		CIDR: testVPCCIDR,
		Tags: map[string]string{
			"test":       "integration",
			"managed-by": "ocfp-test",
		},
	})
	if err != nil {
		t.Fatalf("Failed to create VPC: %v", err)
	}
	if vpc == nil || vpc.ID == "" {
		t.Fatal("Created VPC has no ID")
	}
	t.Logf("Created VPC: %s", vpc.ID)

	// Ensure cleanup
	defer func() {
		t.Logf("Cleaning up VPC: %s", vpc.ID)
		ctx := context.Background()
		if err := nm.DeleteNetwork(ctx, vpc.ID); err != nil {
			t.Logf("Warning: Failed to cleanup VPC %s: %v", vpc.ID, err)
		}
	}()

	// Get VPC
	t.Logf("Getting VPC: %s", vpc.ID)
	retrievedVPC, err := nm.GetNetwork(ctx, vpc.ID)
	if err != nil {
		t.Fatalf("Failed to get VPC: %v", err)
	}
	if retrievedVPC.ID != vpc.ID {
		t.Errorf("Expected VPC ID %s, got %s", vpc.ID, retrievedVPC.ID)
	}
	if retrievedVPC.CIDR != testVPCCIDR {
		t.Errorf("Expected CIDR %s, got %s", testVPCCIDR, retrievedVPC.CIDR)
	}

	// List VPCs
	t.Log("Listing VPCs")
	vpcs, err := nm.ListNetworks(ctx, map[string]string{"managed-by": "ocfp-test"})
	if err != nil {
		t.Fatalf("Failed to list VPCs: %v", err)
	}

	found := false
	for _, v := range vpcs {
		if v.ID == vpc.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("Created VPC not found in list")
	}

	t.Logf("VPC lifecycle test completed successfully")
}

// TestIntegration_SubnetLifecycle tests the full subnet lifecycle
func TestIntegration_SubnetLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client, cleanup := setupTestClient(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	nm := client.NetworkManager()

	// Create VPC first
	vpcName := generateTestName("vpc")
	t.Logf("Creating VPC for subnet test: %s", vpcName)

	vpc, err := nm.CreateNetwork(ctx, &cpi.NetworkRequest{
		Name: vpcName,
		CIDR: testVPCCIDR,
		Tags: map[string]string{"test": "integration"},
	})
	if err != nil {
		t.Fatalf("Failed to create VPC: %v", err)
	}
	defer func() {
		ctx := context.Background()
		_ = nm.DeleteNetwork(ctx, vpc.ID)
	}()

	// Create Subnet
	subnetName := generateTestName("subnet")
	t.Logf("Creating subnet: %s", subnetName)

	subnet, err := nm.CreateSubnet(ctx, &cpi.SubnetRequest{
		Name:             subnetName,
		NetworkID:        vpc.ID,
		CIDR:             testSubnetCIDR,
		AvailabilityZone: testAZ,
		Tags:             map[string]string{"test": "integration"},
	})
	if err != nil {
		t.Fatalf("Failed to create subnet: %v", err)
	}
	if subnet == nil || subnet.ID == "" {
		t.Fatal("Created subnet has no ID")
	}
	t.Logf("Created subnet: %s", subnet.ID)

	// Ensure cleanup
	defer func() {
		t.Logf("Cleaning up subnet: %s", subnet.ID)
		ctx := context.Background()
		if err := nm.DeleteSubnet(ctx, subnet.ID); err != nil {
			t.Logf("Warning: Failed to cleanup subnet %s: %v", subnet.ID, err)
		}
	}()

	// Get Subnet
	t.Logf("Getting subnet: %s", subnet.ID)
	retrievedSubnet, err := nm.GetSubnet(ctx, subnet.ID)
	if err != nil {
		t.Fatalf("Failed to get subnet: %v", err)
	}
	if retrievedSubnet.ID != subnet.ID {
		t.Errorf("Expected subnet ID %s, got %s", subnet.ID, retrievedSubnet.ID)
	}
	if retrievedSubnet.CIDR != testSubnetCIDR {
		t.Errorf("Expected CIDR %s, got %s", testSubnetCIDR, retrievedSubnet.CIDR)
	}

	// List Subnets
	t.Log("Listing subnets")
	subnets, err := nm.ListSubnets(ctx, vpc.ID)
	if err != nil {
		t.Fatalf("Failed to list subnets: %v", err)
	}

	found := false
	for _, s := range subnets {
		if s.ID == subnet.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("Created subnet not found in list")
	}

	t.Logf("Subnet lifecycle test completed successfully")
}

// TestIntegration_FloatingIPLifecycle tests Elastic IP allocation and release
func TestIntegration_FloatingIPLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client, cleanup := setupTestClient(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	nm := client.NetworkManager()

	// Allocate Elastic IP
	t.Log("Allocating Elastic IP")
	eip, err := nm.AllocateFloatingIP(ctx, &cpi.AllocateFloatingIPRequest{})
	if err != nil {
		t.Fatalf("Failed to allocate Elastic IP: %v", err)
	}
	if eip == nil || eip.ID == "" {
		t.Fatal("Allocated Elastic IP has no ID")
	}
	t.Logf("Allocated Elastic IP: %s (%s)", eip.ID, eip.Address)

	// Ensure cleanup
	defer func() {
		t.Logf("Releasing Elastic IP: %s", eip.ID)
		ctx := context.Background()
		if err := nm.ReleaseFloatingIP(ctx, eip.ID); err != nil {
			t.Logf("Warning: Failed to release Elastic IP %s: %v", eip.ID, err)
		}
	}()

	// Get Elastic IP
	t.Logf("Getting Elastic IP: %s", eip.ID)
	retrievedEIP, err := nm.GetFloatingIP(ctx, eip.ID)
	if err != nil {
		t.Fatalf("Failed to get Elastic IP: %v", err)
	}
	if retrievedEIP.ID != eip.ID {
		t.Errorf("Expected Elastic IP ID %s, got %s", eip.ID, retrievedEIP.ID)
	}

	// List Elastic IPs
	t.Log("Listing Elastic IPs")
	eips, err := nm.ListFloatingIPs(ctx)
	if err != nil {
		t.Fatalf("Failed to list Elastic IPs: %v", err)
	}

	found := false
	for _, e := range eips {
		if e.ID == eip.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("Allocated Elastic IP not found in list")
	}

	t.Logf("Elastic IP lifecycle test completed successfully")
}

// TestIntegration_EC2KeyPairLifecycle tests SSH key pair operations
func TestIntegration_EC2KeyPairLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client, cleanup := setupTestClient(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	cm := client.ComputeManager()

	// Create Key Pair
	keyName := generateTestName("key")
	t.Logf("Creating key pair: %s", keyName)

	keyPair, err := cm.CreateKeyPair(ctx, &cpi.KeyPairRequest{
		Name: keyName,
		Tags: map[string]string{"test": "integration"},
	})
	if err != nil {
		t.Fatalf("Failed to create key pair: %v", err)
	}
	if keyPair == nil || keyPair.Name == "" {
		t.Fatal("Created key pair has no name")
	}
	if keyPair.PrivateKey == "" {
		t.Fatal("Created key pair has no private key")
	}
	t.Logf("Created key pair: %s", keyPair.Name)

	// Ensure cleanup
	defer func() {
		t.Logf("Deleting key pair: %s", keyName)
		ctx := context.Background()
		if err := cm.DeleteKeyPair(ctx, keyName); err != nil {
			t.Logf("Warning: Failed to delete key pair %s: %v", keyName, err)
		}
	}()

	// Get Key Pair
	t.Logf("Getting key pair: %s", keyName)
	retrievedKey, err := cm.GetKeyPair(ctx, keyName)
	if err != nil {
		t.Fatalf("Failed to get key pair: %v", err)
	}
	if retrievedKey.Name != keyName {
		t.Errorf("Expected key name %s, got %s", keyName, retrievedKey.Name)
	}

	// List Key Pairs
	t.Log("Listing key pairs")
	keys, err := cm.ListKeyPairs(ctx)
	if err != nil {
		t.Fatalf("Failed to list key pairs: %v", err)
	}

	found := false
	for _, k := range keys {
		if k.Name == keyName {
			found = true
			break
		}
	}
	if !found {
		t.Error("Created key pair not found in list")
	}

	t.Logf("Key pair lifecycle test completed successfully")
}

// TestIntegration_SecurityGroupLifecycle tests security group operations
func TestIntegration_SecurityGroupLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client, cleanup := setupTestClient(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	nm := client.NetworkManager()
	sm := client.SecurityManager()

	// Create VPC first
	vpcName := generateTestName("vpc")
	t.Logf("Creating VPC for security group test: %s", vpcName)

	vpc, err := nm.CreateNetwork(ctx, &cpi.NetworkRequest{
		Name: vpcName,
		CIDR: testVPCCIDR,
		Tags: map[string]string{"test": "integration"},
	})
	if err != nil {
		t.Fatalf("Failed to create VPC: %v", err)
	}
	defer func() {
		ctx := context.Background()
		_ = nm.DeleteNetwork(ctx, vpc.ID)
	}()

	// Create Security Group
	sgName := generateTestName("sg")
	t.Logf("Creating security group: %s", sgName)

	sg, err := sm.CreateSecurityGroup(ctx, &cpi.CreateSecurityGroupRequest{
		Name:        sgName,
		Description: "Integration test security group",
		NetworkID:   vpc.ID,
		Tags:        map[string]string{"test": "integration"},
	})
	if err != nil {
		t.Fatalf("Failed to create security group: %v", err)
	}
	if sg == nil || sg.ID == "" {
		t.Fatal("Created security group has no ID")
	}
	t.Logf("Created security group: %s", sg.ID)

	// Ensure cleanup
	defer func() {
		t.Logf("Deleting security group: %s", sg.ID)
		ctx := context.Background()
		if err := sm.DeleteSecurityGroup(ctx, sg.ID); err != nil {
			t.Logf("Warning: Failed to delete security group %s: %v", sg.ID, err)
		}
	}()

	// Add ingress rule
	t.Log("Adding ingress rule")
	err = sm.AddSecurityRule(ctx, sg.ID, &cpi.SecurityRule{
		Direction:    "ingress",
		Protocol:     "tcp",
		PortRangeMin: 22,
		PortRangeMax: 22,
		RemoteIPCIDR: "0.0.0.0/0",
	})
	if err != nil {
		t.Fatalf("Failed to add security rule: %v", err)
	}

	// Get Security Group
	t.Logf("Getting security group: %s", sg.ID)
	retrievedSG, err := sm.GetSecurityGroup(ctx, sg.ID)
	if err != nil {
		t.Fatalf("Failed to get security group: %v", err)
	}
	if retrievedSG.ID != sg.ID {
		t.Errorf("Expected security group ID %s, got %s", sg.ID, retrievedSG.ID)
	}

	// Verify rule was added
	if len(retrievedSG.Rules) == 0 {
		t.Error("Expected at least one security rule")
	}

	t.Logf("Security group lifecycle test completed successfully")
}

// TestIntegration_EBSVolumeLifecycle tests EBS volume operations
func TestIntegration_EBSVolumeLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client, cleanup := setupTestClient(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	sm := client.StorageManager()

	// Create Volume
	volumeName := generateTestName("vol")
	t.Logf("Creating EBS volume: %s", volumeName)

	volume, err := sm.CreateVolume(ctx, &cpi.VolumeRequest{
		Name:             volumeName,
		SizeGB:           10,
		VolumeType:       "gp3",
		AvailabilityZone: testAZ,
		Tags:             map[string]string{"test": "integration"},
	})
	if err != nil {
		t.Fatalf("Failed to create volume: %v", err)
	}
	if volume == nil || volume.ID == "" {
		t.Fatal("Created volume has no ID")
	}
	t.Logf("Created volume: %s", volume.ID)

	// Ensure cleanup
	defer func() {
		t.Logf("Deleting volume: %s", volume.ID)
		ctx := context.Background()
		if err := sm.DeleteVolume(ctx, volume.ID); err != nil {
			t.Logf("Warning: Failed to delete volume %s: %v", volume.ID, err)
		}
	}()

	// Wait for volume to be available
	t.Log("Waiting for volume to be available")
	time.Sleep(5 * time.Second)

	// Get Volume
	t.Logf("Getting volume: %s", volume.ID)
	retrievedVol, err := sm.GetVolume(ctx, volume.ID)
	if err != nil {
		t.Fatalf("Failed to get volume: %v", err)
	}
	if retrievedVol.ID != volume.ID {
		t.Errorf("Expected volume ID %s, got %s", volume.ID, retrievedVol.ID)
	}
	if retrievedVol.Size != 10 {
		t.Errorf("Expected size 10GB, got %dGB", retrievedVol.Size)
	}

	// List Volumes
	t.Log("Listing volumes")
	volumes, err := sm.ListVolumes(ctx, map[string]string{"test": "integration"})
	if err != nil {
		t.Fatalf("Failed to list volumes: %v", err)
	}

	found := false
	for _, v := range volumes {
		if v.ID == volume.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("Created volume not found in list")
	}

	t.Logf("EBS volume lifecycle test completed successfully")
}

// TestIntegration_EBSSnapshotLifecycle tests EBS snapshot operations
func TestIntegration_EBSSnapshotLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client, cleanup := setupTestClient(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	sm := client.StorageManager()

	// Create Volume first
	volumeName := generateTestName("vol")
	t.Logf("Creating EBS volume: %s", volumeName)

	volume, err := sm.CreateVolume(ctx, &cpi.VolumeRequest{
		Name:             volumeName,
		SizeGB:           10,
		VolumeType:       "gp3",
		AvailabilityZone: testAZ,
		Tags:             map[string]string{"test": "integration"},
	})
	if err != nil {
		t.Fatalf("Failed to create volume: %v", err)
	}
	defer func() {
		ctx := context.Background()
		_ = sm.DeleteVolume(ctx, volume.ID)
	}()

	// Wait for volume to be available
	time.Sleep(5 * time.Second)

	// Create Snapshot
	snapshotName := generateTestName("snap")
	t.Logf("Creating snapshot: %s", snapshotName)

	snapshot, err := sm.CreateSnapshot(ctx, volume.ID, snapshotName)
	if err != nil {
		t.Fatalf("Failed to create snapshot: %v", err)
	}
	if snapshot == nil || snapshot.ID == "" {
		t.Fatal("Created snapshot has no ID")
	}
	t.Logf("Created snapshot: %s", snapshot.ID)

	// Ensure cleanup
	defer func() {
		t.Logf("Deleting snapshot: %s", snapshot.ID)
		ctx := context.Background()
		if err := sm.DeleteSnapshot(ctx, snapshot.ID); err != nil {
			t.Logf("Warning: Failed to delete snapshot %s: %v", snapshot.ID, err)
		}
	}()

	// Get Snapshot
	t.Logf("Getting snapshot: %s", snapshot.ID)
	retrievedSnap, err := sm.GetSnapshot(ctx, snapshot.ID)
	if err != nil {
		t.Fatalf("Failed to get snapshot: %v", err)
	}
	if retrievedSnap.ID != snapshot.ID {
		t.Errorf("Expected snapshot ID %s, got %s", snapshot.ID, retrievedSnap.ID)
	}

	// List Snapshots
	t.Log("Listing snapshots")
	snapshots, err := sm.ListSnapshots(ctx, volume.ID, nil)
	if err != nil {
		t.Fatalf("Failed to list snapshots: %v", err)
	}

	found := false
	for _, s := range snapshots {
		if s.ID == snapshot.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("Created snapshot not found in list")
	}

	t.Logf("EBS snapshot lifecycle test completed successfully")
}

// TestIntegration_S3BucketLifecycle tests S3 bucket operations
func TestIntegration_S3BucketLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client, cleanup := setupTestClient(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	sm := client.StorageManager()

	// Create Bucket (must be globally unique)
	bucketName := fmt.Sprintf("%s-%d", testNamePrefix, time.Now().UnixNano())
	t.Logf("Creating S3 bucket: %s", bucketName)

	bucket, err := sm.CreateBucket(ctx, &cpi.BucketRequest{
		Name: bucketName,
		Tags: map[string]string{"test": "integration"},
	})
	if err != nil {
		t.Fatalf("Failed to create bucket: %v", err)
	}
	if bucket == nil || bucket.Name == "" {
		t.Fatal("Created bucket has no name")
	}
	t.Logf("Created bucket: %s", bucket.Name)

	// Ensure cleanup
	defer func() {
		t.Logf("Deleting bucket: %s", bucketName)
		ctx := context.Background()
		// Empty bucket first
		if err := sm.EmptyBucket(ctx, bucketName); err != nil {
			t.Logf("Warning: Failed to empty bucket %s: %v", bucketName, err)
		}
		if err := sm.DeleteBucket(ctx, bucketName); err != nil {
			t.Logf("Warning: Failed to delete bucket %s: %v", bucketName, err)
		}
	}()

	// Get Bucket
	t.Logf("Getting bucket: %s", bucketName)
	retrievedBucket, err := sm.GetBucket(ctx, bucketName)
	if err != nil {
		t.Fatalf("Failed to get bucket: %v", err)
	}
	if retrievedBucket.Name != bucketName {
		t.Errorf("Expected bucket name %s, got %s", bucketName, retrievedBucket.Name)
	}

	// List Buckets
	t.Log("Listing buckets")
	buckets, err := sm.ListBuckets(ctx)
	if err != nil {
		t.Fatalf("Failed to list buckets: %v", err)
	}

	found := false
	for _, b := range buckets {
		if b.Name == bucketName {
			found = true
			break
		}
	}
	if !found {
		t.Error("Created bucket not found in list")
	}

	t.Logf("S3 bucket lifecycle test completed successfully")
}

// TestIntegration_LoadBalancerLifecycle tests load balancer operations
func TestIntegration_LoadBalancerLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client, cleanup := setupTestClient(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	nm := client.NetworkManager()
	lbm := client.LoadBalancerManager()

	// Create VPC and Subnets first (need at least 2 subnets in different AZs for ALB)
	vpcName := generateTestName("vpc")
	t.Logf("Creating VPC for load balancer test: %s", vpcName)

	vpc, err := nm.CreateNetwork(ctx, &cpi.NetworkRequest{
		Name: vpcName,
		CIDR: testVPCCIDR,
		Tags: map[string]string{"test": "integration"},
	})
	if err != nil {
		t.Fatalf("Failed to create VPC: %v", err)
	}
	defer func() {
		ctx := context.Background()
		_ = nm.DeleteNetwork(ctx, vpc.ID)
	}()

	// Create subnet in first AZ
	subnet1Name := generateTestName("subnet1")
	subnet1, err := nm.CreateSubnet(ctx, &cpi.SubnetRequest{
		Name:             subnet1Name,
		NetworkID:        vpc.ID,
		CIDR:             "10.100.1.0/24",
		AvailabilityZone: "us-west-2a",
		Tags:             map[string]string{"test": "integration"},
	})
	if err != nil {
		t.Fatalf("Failed to create subnet 1: %v", err)
	}
	defer func() {
		ctx := context.Background()
		_ = nm.DeleteSubnet(ctx, subnet1.ID)
	}()

	// Create subnet in second AZ
	subnet2Name := generateTestName("subnet2")
	subnet2, err := nm.CreateSubnet(ctx, &cpi.SubnetRequest{
		Name:             subnet2Name,
		NetworkID:        vpc.ID,
		CIDR:             "10.100.2.0/24",
		AvailabilityZone: "us-west-2b",
		Tags:             map[string]string{"test": "integration"},
	})
	if err != nil {
		t.Fatalf("Failed to create subnet 2: %v", err)
	}
	defer func() {
		ctx := context.Background()
		_ = nm.DeleteSubnet(ctx, subnet2.ID)
	}()

	// Create Load Balancer
	lbName := generateTestName("lb")
	t.Logf("Creating load balancer: %s", lbName)

	lb, err := lbm.CreateLoadBalancer(ctx, &cpi.CreateLoadBalancerRequest{
		Name:      lbName,
		Type:      "application",
		Scheme:    "internet-facing",
		SubnetIDs: []string{subnet1.ID, subnet2.ID},
		Tags:      map[string]string{"test": "integration"},
	})
	if err != nil {
		t.Fatalf("Failed to create load balancer: %v", err)
	}
	if lb == nil || lb.ID == "" {
		t.Fatal("Created load balancer has no ID")
	}
	t.Logf("Created load balancer: %s", lb.ID)

	// Ensure cleanup
	defer func() {
		t.Logf("Deleting load balancer: %s", lb.ID)
		ctx := context.Background()
		if err := lbm.DeleteLoadBalancer(ctx, lb.ID); err != nil {
			t.Logf("Warning: Failed to delete load balancer %s: %v", lb.ID, err)
		}
	}()

	// Wait for LB to provision
	t.Log("Waiting for load balancer to provision")
	time.Sleep(10 * time.Second)

	// Get Load Balancer
	t.Logf("Getting load balancer: %s", lb.ID)
	retrievedLB, err := lbm.GetLoadBalancer(ctx, lb.ID)
	if err != nil {
		t.Fatalf("Failed to get load balancer: %v", err)
	}
	if retrievedLB.ID != lb.ID {
		t.Errorf("Expected load balancer ID %s, got %s", lb.ID, retrievedLB.ID)
	}

	// List Load Balancers
	t.Log("Listing load balancers")
	lbs, err := lbm.ListLoadBalancers(ctx, map[string]string{"test": "integration"})
	if err != nil {
		t.Fatalf("Failed to list load balancers: %v", err)
	}

	found := false
	for _, l := range lbs {
		if l.ID == lb.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("Created load balancer not found in list")
	}

	t.Logf("Load balancer lifecycle test completed successfully")
}

// TestIntegration_CrossServiceScenario tests a realistic cross-service scenario
func TestIntegration_CrossServiceScenario(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	client, cleanup := setupTestClient(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	nm := client.NetworkManager()
	cm := client.ComputeManager()
	sm := client.SecurityManager()
	stm := client.StorageManager()

	t.Log("=== Cross-Service Integration Test ===")

	// Step 1: Create VPC
	vpcName := generateTestName("vpc")
	t.Logf("Step 1: Creating VPC: %s", vpcName)
	vpc, err := nm.CreateNetwork(ctx, &cpi.NetworkRequest{
		Name: vpcName,
		CIDR: testVPCCIDR,
		Tags: map[string]string{"test": "cross-service"},
	})
	if err != nil {
		t.Fatalf("Failed to create VPC: %v", err)
	}
	t.Logf("✓ Created VPC: %s", vpc.ID)

	defer func() {
		ctx := context.Background()
		_ = nm.DeleteNetwork(ctx, vpc.ID)
	}()

	// Step 2: Create Subnet
	subnetName := generateTestName("subnet")
	t.Logf("Step 2: Creating subnet: %s", subnetName)
	subnet, err := nm.CreateSubnet(ctx, &cpi.SubnetRequest{
		Name:             subnetName,
		NetworkID:        vpc.ID,
		CIDR:             testSubnetCIDR,
		AvailabilityZone: testAZ,
		Tags:             map[string]string{"test": "cross-service"},
	})
	if err != nil {
		t.Fatalf("Failed to create subnet: %v", err)
	}
	t.Logf("✓ Created subnet: %s", subnet.ID)

	defer func() {
		ctx := context.Background()
		_ = nm.DeleteSubnet(ctx, subnet.ID)
	}()

	// Step 3: Create Security Group
	sgName := generateTestName("sg")
	t.Logf("Step 3: Creating security group: %s", sgName)
	sg, err := sm.CreateSecurityGroup(ctx, &cpi.CreateSecurityGroupRequest{
		Name:        sgName,
		Description: "Cross-service test security group",
		NetworkID:   vpc.ID,
		Tags:        map[string]string{"test": "cross-service"},
	})
	if err != nil {
		t.Fatalf("Failed to create security group: %v", err)
	}
	t.Logf("✓ Created security group: %s", sg.ID)

	defer func() {
		ctx := context.Background()
		_ = sm.DeleteSecurityGroup(ctx, sg.ID)
	}()

	// Step 4: Add SSH rule to security group
	t.Log("Step 4: Adding SSH rule to security group")
	err = sm.AddSecurityRule(ctx, sg.ID, &cpi.SecurityRule{
		Direction:    "ingress",
		Protocol:     "tcp",
		PortRangeMin: 22,
		PortRangeMax: 22,
		RemoteIPCIDR: "0.0.0.0/0",
	})
	if err != nil {
		t.Fatalf("Failed to add security rule: %v", err)
	}
	t.Log("✓ Added SSH rule")

	// Step 5: Create Key Pair
	keyName := generateTestName("key")
	t.Logf("Step 5: Creating key pair: %s", keyName)
	keyPair, err := cm.CreateKeyPair(ctx, &cpi.KeyPairRequest{
		Name: keyName,
		Tags: map[string]string{"test": "cross-service"},
	})
	if err != nil {
		t.Fatalf("Failed to create key pair: %v", err)
	}
	t.Logf("✓ Created key pair: %s", keyPair.Name)

	defer func() {
		ctx := context.Background()
		_ = cm.DeleteKeyPair(ctx, keyName)
	}()

	// Step 6: Create EBS Volume
	volumeName := generateTestName("vol")
	t.Logf("Step 6: Creating EBS volume: %s", volumeName)
	volume, err := stm.CreateVolume(ctx, &cpi.VolumeRequest{
		Name:             volumeName,
		SizeGB:           10,
		VolumeType:       "gp3",
		AvailabilityZone: testAZ,
		Tags:             map[string]string{"test": "cross-service"},
	})
	if err != nil {
		t.Fatalf("Failed to create volume: %v", err)
	}
	t.Logf("✓ Created volume: %s", volume.ID)

	defer func() {
		ctx := context.Background()
		_ = stm.DeleteVolume(ctx, volume.ID)
	}()

	// Step 7: Allocate Elastic IP
	t.Log("Step 7: Allocating Elastic IP")
	eip, err := nm.AllocateFloatingIP(ctx, &cpi.AllocateFloatingIPRequest{})
	if err != nil {
		t.Fatalf("Failed to allocate Elastic IP: %v", err)
	}
	t.Logf("✓ Allocated Elastic IP: %s", eip.Address)

	defer func() {
		ctx := context.Background()
		_ = nm.ReleaseFloatingIP(ctx, eip.ID)
	}()

	t.Log("=== Cross-Service Test Completed Successfully ===")
	t.Log("Summary:")
	t.Logf("  - VPC: %s", vpc.ID)
	t.Logf("  - Subnet: %s", subnet.ID)
	t.Logf("  - Security Group: %s", sg.ID)
	t.Logf("  - Key Pair: %s", keyPair.Name)
	t.Logf("  - Volume: %s", volume.ID)
	t.Logf("  - Elastic IP: %s", eip.Address)
}
