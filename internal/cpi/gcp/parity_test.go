package gcp

import (
	"cloud.google.com/go/compute/apiv1/computepb"
	"context"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"testing"
)

func TestParityInstanceMetadata(t *testing.T) {
	m := &ComputeManager{}
	req := &cpi.InstanceRequest{UserData: "#cloud-config\nhostname: bastion", PublicKey: "ssh-ed25519 AAAA test", DefaultUsername: "operator"}
	inst := &computepb.Instance{}
	m.buildInstanceMetadata(inst, req)
	values := map[string]string{}
	for _, item := range inst.GetMetadata().GetItems() {
		values[item.GetKey()] = item.GetValue()
	}
	if values["user-data"] != req.UserData || values["startup-script"] != "" {
		t.Fatalf("cloud config delivered incorrectly: %v", values)
	}
	if values["ssh-keys"] != "operator:"+req.PublicKey {
		t.Fatalf("missing SSH key: %v", values)
	}
}
func TestParityStaticPrivateAddress(t *testing.T) {
	m := &ComputeManager{}
	nics := m.buildNetworkInterfaces(&Config{ProjectID: "p", Region: "r"}, &cpi.InstanceRequest{SubnetID: "subnet", StaticPrivateIP: "10.1.2.3"})
	if nics[0].GetNetworkIP() != "10.1.2.3" {
		t.Fatal("static IP ignored")
	}
}
func TestParityRegion(t *testing.T) {
	for input, want := range map[string]string{"us-central1": "us-central1", "us-central1-a": "us-central1", "europe-west12-b": "europe-west12"} {
		if got := GetRegionFromZone(input); got != want {
			t.Errorf("%s => %s want %s", input, got, want)
		}
	}
}
func TestParityLabels(t *testing.T) {
	labels := BuildLabels("VM", map[string]string{"ocfp:bloc": "My.Bloc"})
	if !matchesLabelFilters(labels, map[string]string{"label.ocfp:bloc": "My.Bloc"}) {
		t.Fatal("write/read label normalization differs")
	}
}
func TestParityGroupIdentity(t *testing.T) {
	group := (&SecurityManager{}).convertFirewallToSecurityGroup(&computepb.Firewall{Name: proto("bastion"), Id: proto(uint64(42))})
	if group.ID != "bastion" {
		t.Fatalf("group id %s cannot reproduce target tag", group.ID)
	}
}
func TestParityStoppedGuard(t *testing.T) {
	_, err := (&ComputeManager{}).CreateInstance(context.Background(), &cpi.InstanceRequest{CreateStopped: true})
	if err == nil {
		t.Fatal("unsafe stopped create accepted")
	}
}

func TestParityScopedIdentity(t *testing.T) {
	m := &ComputeManager{client: &Client{config: &Config{ProjectID: "p", Zone: "r-a"}}}
	inst := m.convertInstance(&computepb.Instance{Name: proto("vm"), Id: proto(uint64(3)), Zone: proto("projects/p/zones/r-b")})
	if inst.ID != "projects/p/zones/r-b/instances/vm" {
		t.Fatalf("lost scope: %s", inst.ID)
	}
	volume := (&StorageManager{client: m.client}).convertDisk(&computepb.Disk{Name: proto("data"), Zone: proto("projects/p/zones/r-b"), Users: []string{"projects/p/zones/r-b/instances/vm"}, Status: proto("READY")})
	if volume.ID != "projects/p/zones/r-b/disks/data" || volume.AttachedTo != inst.ID || volume.State != cpi.ResourceStateInUse {
		t.Fatalf("lost disk scope/attachment: %+v", volume)
	}
}
func TestParityLocalKeypair(t *testing.T) {
	m := &ComputeManager{}
	if key, err := m.GetKeyPair(context.Background(), "missing"); err == nil || key != nil {
		t.Fatal("invented cloud keypair")
	}
	if key, err := m.CreateKeyPair(context.Background(), &cpi.KeyPairRequest{Name: "key"}); err == nil || key != nil {
		t.Fatal("invented cloud keypair generation")
	}
}

func TestParityUnsafeLoadBalancerGuard(t *testing.T) {
	if _, err := (&LoadBalancerManager{}).CreateLoadBalancer(context.Background(), &cpi.CreateLoadBalancerRequest{Name: "lb"}); err == nil {
		t.Fatal("invalid provider graph accepted")
	}
}
func TestParityResourceNames(t *testing.T) {
	m := &ComputeManager{}
	image := m.convertImage(&computepb.Image{Id: proto(uint64(10)), Name: proto("ubuntu"), SelfLink: proto("https://www.googleapis.com/compute/v1/projects/images/global/images/ubuntu")})
	if image.ID != "https://www.googleapis.com/compute/v1/projects/images/global/images/ubuntu" {
		t.Fatal("image project provenance lost")
	}
	flavor := m.convertMachineType(&computepb.MachineType{Id: proto(uint64(20)), Name: proto("e2-small")})
	if flavor.ID != "e2-small" {
		t.Fatal("machine type name lost")
	}
	snapshot := (&StorageManager{}).convertSnapshot(&computepb.Snapshot{Id: proto(uint64(30)), Name: proto("backup"), SourceDisk: proto("projects/p/zones/r-b/disks/data"), Status: proto("READY")})
	if snapshot.ID != "backup" || snapshot.State != cpi.ResourceStateAvailable || snapshot.VolumeID != "projects/p/zones/r-b/disks/data" {
		t.Fatal("snapshot identity/state unusable")
	}
}

func TestParityPublicIPWithoutExplicitSubnet(t *testing.T) {
	public := true
	nics := (&ComputeManager{}).buildNetworkInterfaces(&Config{ProjectID: "p", Region: "r"}, &cpi.InstanceRequest{NetworkID: "network", PublicIP: &public})
	if len(nics) != 1 || len(nics[0].GetAccessConfigs()) != 1 {
		t.Fatal("explicit public IP ignored on network-only request")
	}
}

func TestParityUnsupportedOverridesFailBeforeMutation(t *testing.T) {
	for _, req := range []*cpi.InstanceRequest{{VCPUsOverride: 4}, {MemoryMiBOverride: 8192}} {
		if _, err := (&ComputeManager{}).CreateInstance(context.Background(), req); err == nil {
			t.Fatal("silently ignored requested override")
		}
	}
}
