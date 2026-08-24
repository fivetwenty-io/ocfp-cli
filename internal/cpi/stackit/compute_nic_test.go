package stackit

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas/v2api"
	"github.com/stretchr/testify/assert"
)

// nicFixture builds a NIC whose own id differs from the network it lives on, so
// that a mix-up between the two is visible in assertions.
func nicFixture(nicID, networkID, ipv4 string) iaas.NIC {
	return iaas.NIC{
		Id:        &nicID,
		NetworkId: &networkID,
		Ipv4:      &ipv4,
	}
}

// The NIC id and the network id are distinct fields; cpi.Instance.NetworkID is
// the network, matching what the other providers put there.
func TestProcessNetworkInterfaceUsesNetworkID(t *testing.T) {
	m := &ComputeManager{}
	nic := nicFixture("nic-uuid", "network-uuid", "10.0.0.5")

	inst := &cpi.Instance{}
	m.processNetworkInterface(nic, map[string]string{"nic-uuid": "203.0.113.7"}, inst)

	assert.Equal(t, "network-uuid", inst.NetworkID)
	assert.Equal(t, "10.0.0.5", inst.PrivateIP)
	assert.Equal(t, "203.0.113.7", inst.PublicIP)
	assert.Equal(t, "203.0.113.7", inst.FloatingIP)
}

func TestProcessNetworkInterfaceFromListUsesNetworkID(t *testing.T) {
	m := &ComputeManager{}
	nic := nicFixture("nic-uuid", "network-uuid", "10.0.0.6")

	inst := &cpi.Instance{}
	m.processNetworkInterfaceFromList(nic, map[string]string{"nic-uuid": "203.0.113.8"}, inst)

	assert.Equal(t, "network-uuid", inst.NetworkID)
	assert.Equal(t, "10.0.0.6", inst.PrivateIP)
	assert.Equal(t, "203.0.113.8", inst.PublicIP)
}

// The first NIC carrying a network id wins; later NICs must not overwrite it.
func TestProcessNetworkInterfacesKeepsFirstNetworkID(t *testing.T) {
	m := &ComputeManager{}
	nics := []iaas.NIC{
		nicFixture("nic-a", "network-a", "10.0.0.1"),
		nicFixture("nic-b", "network-b", "10.0.0.2"),
	}

	inst := &cpi.Instance{}
	m.processNetworkInterfaces(nics, map[string]string{}, inst)

	assert.Equal(t, "network-a", inst.NetworkID)
	assert.Equal(t, "10.0.0.1", inst.PrivateIP)
}
