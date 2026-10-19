package gcp

import (
	"cloud.google.com/go/compute/apiv1/computepb"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"testing"
)

func TestParityNetworkOwnership(t *testing.T) {
	description := resourceDescription("human description", map[string]string{"ocfp:bloc": "bloc-a", "managed-by": "ocfp"})
	if !matchesFilters(description, map[string]string{"ocfp:bloc": "bloc-a", "managed-by": "ocfp"}) {
		t.Fatal("owned network lost")
	}
	if matchesFilters(description, map[string]string{"ocfp:bloc": "bloc-a", "managed-by": "someone-else"}) {
		t.Fatal("filter matches OR rather than AND")
	}
	if matchesFilters("someone mentioned bloc-a", map[string]string{"ocfp:bloc": "bloc-a"}) {
		t.Fatal("adopts unowned substring")
	}
	network := (&NetworkManager{}).convertNetwork(&computepb.Network{Name: proto("net"), Description: proto(description)})
	if network.Tags["ocfp:bloc"] != "bloc-a" {
		t.Fatal("network tags lost")
	}
}
func TestParityInstanceNetworkDiscovery(t *testing.T) {
	m := &ComputeManager{client: &Client{config: &Config{ProjectID: "p", Zone: "r-a"}}}
	vm := m.convertInstance(&computepb.Instance{Name: proto("vm"), NetworkInterfaces: []*computepb.NetworkInterface{{Network: proto("projects/p/global/networks/net"), Subnetwork: proto("projects/p/regions/r/subnetworks/sub"), NetworkIP: proto("10.0.0.1")}}, Labels: BuildLabels("vm", map[string]string{"ocfp:role": "bastion"})})
	if vm.NetworkID != "net" || vm.SubnetID != "sub" || vm.Tags["ocfp:role"] != "bastion" {
		t.Fatalf("discovery loses data: %+v", vm)
	}
}
func TestParityFirewallRejectsInvalidCIDR(t *testing.T) {
	if _, err := makeFirewall("group", "net", &cpi.SecurityRule{Direction: "ingress", Protocol: "tcp", PortRangeMin: 22, PortRangeMax: 22, RemoteIPCIDR: "bad"}); err == nil {
		t.Fatal("invalid CIDR accepted")
	}
}

func TestParityFirewallPortZeroRange(t *testing.T) {
	firewall, err := makeFirewall("group", "net", &cpi.SecurityRule{Direction: "ingress", Protocol: "tcp", PortRangeMin: 0, PortRangeMax: 80})
	if err != nil {
		t.Fatal(err)
	}
	if len(firewall.GetAllowed()[0].GetPorts()) != 1 || firewall.GetAllowed()[0].GetPorts()[0] != "0-80" {
		t.Fatal("broadened bounded range to all ports")
	}
}
