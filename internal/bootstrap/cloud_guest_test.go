package bootstrap

import (
	"context"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"strings"
	"testing"
)

func TestCloudBastionRequestDelivery(t *testing.T) {
	for _, provider := range []string{"aws", "gcp"} {
		m := &Manager{config: &config.Config{}, options: &Options{Provider: provider}}
		req := &cpi.InstanceRequest{DefaultUsername: "ubuntu", Ingress: &cpi.IngressSpec{OriginIP: "10.0.0.10"}}
		if err := m.prepareCloudBastion(req); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(req.UserData, "gzip -d") {
			t.Fatal("cloud services not delivered")
		}
		if req.PublicIP == nil || !*req.PublicIP {
			t.Fatal("cloud bastion requires explicit public connectivity by default")
		}
		if !req.IPForwarding {
			t.Fatal("ingress bastion must forward")
		}
	}
}

func TestCloudRecycleRefusesBeforeProviderAccess(t *testing.T) {
	for _, provider := range []string{"aws", "gcp"} {
		m := &Manager{config: &config.Config{}, options: &Options{Provider: provider}}
		cluster := NewBastionRecycleCluster(m, "")
		if _, err := cluster.Observe(context.Background()); err == nil {
			t.Fatal("unsafe cloud recycle accepted")
		}
	}
}

func TestAWSBastionAvoidsReservedCloudAddresses(t *testing.T) {
	if got := cloudBastionIP("aws", "10.0.4.0/22", "10.0.4.3"); got != "10.0.7.254" {
		t.Fatalf("AWS address = %s", got)
	}
	if got := cloudBastionIP("gcp", "10.0.4.0/22", "10.0.4.3"); got != "10.0.4.3" {
		t.Fatalf("GCP address = %s", got)
	}
	if got := cloudBastionIP("aws", "10.0.4.0/22", "10.0.4.20"); got != "10.0.4.20" {
		t.Fatalf("custom address = %s", got)
	}
}

func TestCloudBootstrapPackagesUseSupportedNames(t *testing.T) {
	if strings.Contains(generateBastionUserData(&config.Config{}), " zlibc ") {
		t.Fatal("obsolete zlibc package prevents supported Ubuntu bootstrap")
	}
}
