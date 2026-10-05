package pve

import (
	"os/exec"
	"strings"
	"testing"
)

// TestCatalog_DracutImagesDisableInitrdNetwork pins which catalog entries
// rebuild their initramfs without network modules. Resolute boots through
// dracut, whose initramfs DHCPs the NIC and leaves the link up, so a clone's
// cloud-init rename to eth0 fails and its static address never applies.
// Noble uses initramfs-tools and has no dracut to run, so the seed step
// would fail there.
func TestCatalog_DracutImagesDisableInitrdNetwork(t *testing.T) {
	t.Parallel()

	want := map[string]bool{
		"ubuntu-noble-template":            false,
		"ubuntu-noble-bastion-template":    false,
		"ubuntu-resolute-template":         true,
		"ubuntu-resolute-bastion-template": true,
	}

	for name, wantDisable := range want {
		spec, ok := LookupCatalogSpec(name)
		if !ok {
			t.Errorf("LookupCatalogSpec(%q) returned ok=false", name)

			continue
		}

		if spec.DisableInitrdNetwork != wantDisable {
			t.Errorf("%s DisableInitrdNetwork = %v, want %v", name, spec.DisableInitrdNetwork, wantDisable)
		}
	}
}

// TestTemplateSpec_NeedsSeed pins that the plain Resolute template is now
// seeded. Before the initramfs fix only bastion templates booted for a seed,
// so a plain template that skipped it would ship the DHCP initramfs.
func TestTemplateSpec_NeedsSeed(t *testing.T) {
	t.Parallel()

	want := map[string]bool{
		"ubuntu-noble-template":            false,
		"ubuntu-noble-bastion-template":    true,
		"ubuntu-resolute-template":         true,
		"ubuntu-resolute-bastion-template": true,
	}

	for name, wantSeed := range want {
		spec, ok := LookupCatalogSpec(name)
		if !ok {
			t.Errorf("LookupCatalogSpec(%q) returned ok=false", name)

			continue
		}

		if got := spec.needsSeed(); got != wantSeed {
			t.Errorf("%s needsSeed() = %v, want %v", name, got, wantSeed)
		}
	}
}

func TestInitrdNoNetworkConf_OmitsNetworkModules(t *testing.T) {
	t.Parallel()

	if !strings.HasPrefix(initrdNoNetworkConfPath, "/etc/dracut.conf.d/") || !strings.HasSuffix(initrdNoNetworkConfPath, ".conf") {
		t.Errorf("initrdNoNetworkConfPath = %q, want a .conf file under /etc/dracut.conf.d/, which is the only place dracut reads drop-ins from", initrdNoNetworkConfPath)
	}

	if !strings.Contains(initrdNoNetworkConf, "omit_dracutmodules+=") {
		t.Fatalf("initrdNoNetworkConf must append to omit_dracutmodules, not assign it, so it composes with other drop-ins")
	}

	for _, module := range []string{"net-lib", "systemd-networkd"} {
		if !strings.Contains(initrdNoNetworkConf, " "+module+" ") {
			t.Errorf("initrdNoNetworkConf must omit %q; every dracut network module depends on net-lib or systemd-networkd", module)
		}
	}
}

func TestInitrdNoNetworkScript_RebuildsAndVerifies(t *testing.T) {
	t.Parallel()

	for _, want := range []string{
		"set -euo pipefail",
		"dracut --quiet --force --regenerate-all --tmpdir",
		// The template disk is only as large as the cloud image, so
		// staging under /var/tmp runs out of space.
		"mount -t tmpfs",
		"lsinitrd -m",
		// An unreadable module list must fail, not pass as "no network
		// modules found".
		"grep -qx systemd",
		"net-lib|systemd-networkd",
	} {
		if !strings.Contains(initrdNoNetworkScript, want) {
			t.Errorf("initrdNoNetworkScript missing %q", want)
		}
	}
}

// TestInitrdNoNetworkScript_ParsesAsBash catches a quoting slip in the
// script before it reaches a template build, where it would only show up as
// a seed failure on the serial console.
func TestInitrdNoNetworkScript_ParsesAsBash(t *testing.T) {
	t.Parallel()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}

	cmd := exec.Command(bash, "-n")
	cmd.Stdin = strings.NewReader(initrdNoNetworkScript)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash -n rejected initrdNoNetworkScript: %v\n%s", err, out)
	}
}
