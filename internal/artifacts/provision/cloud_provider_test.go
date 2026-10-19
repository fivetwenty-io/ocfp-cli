package provision

import (
	"strings"
	"testing"
)

func TestCloudArtifactsUsesStableDiskAndSingleInstaller(t *testing.T) {
	for _, provider := range []string{"aws", "gcp"} {
		in := validArtifactsInputs()
		in.Provider = provider
		in.Filesystem = "ext4"
		script, err := RenderArtifactsProvisionScript(in)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(script, "/dev/sdb") {
			t.Fatal("cloud disk must not depend on enumeration")
		}
		for _, want := range []string{"ocfp-disk-resolve artifacts", "wipefs", "unexpected filesystem"} {
			if !strings.Contains(script, want) {
				t.Errorf("missing %s", want)
			}
		}
		data, err := RenderArtifactsCloudInit(in)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(data, "ocfp-artifacts-install") {
			t.Fatal("cloud-init must deliver installer")
		}
	}
}

func TestCloudArtifactsZFSScopesPoolToDataDisk(t *testing.T) {
	in := validArtifactsInputs()
	in.Provider = "aws"
	script, err := RenderArtifactsProvisionScript(in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, "zpool import -a") {
		t.Fatal("must not import unrelated pools")
	}
	for _, want := range []string{"zpool status -P", "zpool import -d", "ocfp-disk-resolve artifacts zfs", "foreign pool"} {
		if !strings.Contains(script, want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestCloudArtifactsMountRequiredBeforeService(t *testing.T) {
	in := validArtifactsInputs()
	in.Provider = "gcp"
	in.Filesystem = "ext4"
	script, err := RenderArtifactsProvisionScript(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"RequiresMountsFor=" + in.Mountpoint, "ConditionPathIsMountPoint=" + in.Mountpoint, "ocfp-artdata", "-s LABEL", "cannot inspect disk signatures", "unexpected mounted artifacts device"} {
		if !strings.Contains(script, want) {
			t.Errorf("missing %s", want)
		}
	}
	data, err := RenderArtifactsCloudInit(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ocfp-artifacts-install.service", "Restart=on-failure", "--no-block"} {
		if !strings.Contains(data, want) {
			t.Errorf("missing retry requirement %s", want)
		}
	}
}

func TestCloudArtifactsCustomSSHIdentity(t *testing.T) {
	in := validArtifactsInputs()
	in.Provider = "aws"
	in.Username = "operator"
	in.PublicKey = "ssh-ed25519 AAA-test"
	in.Hostname = "artifacts.example.net"
	data, err := RenderArtifactsCloudInit(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name: operator", "ssh-ed25519 AAA-test", "NOPASSWD:ALL", "fqdn: artifacts.example.net"} {
		if !strings.Contains(data, want) {
			t.Errorf("missing custom identity %s", want)
		}
	}
}
