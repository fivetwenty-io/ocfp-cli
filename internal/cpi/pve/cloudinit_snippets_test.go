package pve

import (
	"fmt"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// TestBuildUserDataSnippet asserts the snippet carries the hostname, FQDN,
// default user, and SSH key as cloud-init expects. Empty optional fields drop
// out of the rendered document so PVE's default cloud-init can still populate
// them when absent.
func TestBuildUserDataSnippet(t *testing.T) {
	t.Parallel()

	const pub = "ssh-ed25519 AAAA-key ocfp"

	req := &cpi.InstanceRequest{
		Name:            "520-pve-wayne-bastion",
		Hostname:        "520-pve-wayne-bastion",
		DomainSuffix:    "lab.fivetwenty.io",
		DefaultUsername: "ubuntu",
		PublicKey:       pub,
	}

	out := string(buildUserDataSnippet(req))

	checks := []string{
		"#cloud-config",
		"hostname: 520-pve-wayne-bastion",
		"fqdn: 520-pve-wayne-bastion.lab.fivetwenty.io",
		"manage_etc_hosts: true",
		"preserve_hostname: false",
		"users:",
		"name: ubuntu",
		"sudo: ALL=(ALL) NOPASSWD:ALL",
		"shell: /bin/bash",
		"ssh_authorized_keys:",
		"- " + pub,
		"chpasswd: { expire: false }",
		"ssh_pwauth: false",
	}

	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("snippet missing %q\n----\n%s", want, out)
		}
	}
}

// TestBuildUserDataSnippetHostnameOnly verifies that when no DomainSuffix is
// configured the FQDN line equals the hostname (no trailing dot).
func TestBuildUserDataSnippetHostnameOnly(t *testing.T) {
	t.Parallel()

	out := string(buildUserDataSnippet(&cpi.InstanceRequest{
		Hostname: "bare-host",
	}))

	if !strings.Contains(out, "hostname: bare-host") {
		t.Errorf("missing hostname line:\n%s", out)
	}

	if !strings.Contains(out, "fqdn: bare-host\n") {
		t.Errorf("fqdn should equal hostname when no domain set:\n%s", out)
	}
}

// TestBuildUserDataSnippet_TailscaleRuncmd asserts that when TailscaleAuthKey
// is set the rendered cloud-config contains a runcmd entry that installs
// tailscale and joins the tailnet with the configured hostname and ocfp tags.
func TestBuildUserDataSnippet_TailscaleRuncmd(t *testing.T) {
	t.Parallel()

	req := &cpi.InstanceRequest{
		Hostname:         "lab-bastion",
		TailscaleAuthKey: "tskey-abc",
	}

	out := string(buildUserDataSnippet(req))

	checks := []string{
		"runcmd:",
		"curl -fsSL https://tailscale.com/install.sh | sh",
		`tailscale up --authkey="tskey-abc" --hostname="lab-bastion" --advertise-tags=tag:ocfp-bastion --ssh`,
	}

	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("snippet missing %q\n----\n%s", want, out)
		}
	}
}

// TestBuildUserDataSnippet_TailscaleAbsentByDefault ensures the tailscale
// install + join block is omitted entirely when no auth key is supplied.
func TestBuildUserDataSnippet_TailscaleAbsentByDefault(t *testing.T) {
	t.Parallel()

	out := string(buildUserDataSnippet(&cpi.InstanceRequest{
		Hostname: "lab-bastion",
	}))

	if strings.Contains(out, "tailscale") {
		t.Errorf("snippet should not mention tailscale when TailscaleAuthKey is empty:\n%s", out)
	}
}

// TestBuildUserDataSnippet_TailscaleAdvertiseRoutes verifies the bastion
// advertises its parent vnet CIDR (derived from StaticPrivateIP +
// StaticPrivateIPPrefix) so tailnet peers can reach bloc-internal VMs once
// the routes are approved in the Tailscale admin.
func TestBuildUserDataSnippet_TailscaleAdvertiseRoutes(t *testing.T) {
	t.Parallel()

	req := &cpi.InstanceRequest{
		Hostname:              "wayne-bastion",
		TailscaleAuthKey:      "tskey-xyz",
		StaticPrivateIP:       "10.64.64.3",
		StaticPrivateIPPrefix: 18,
	}

	out := string(buildUserDataSnippet(req))

	if !strings.Contains(out, "--advertise-routes=10.64.64.0/18") {
		t.Errorf("snippet missing --advertise-routes flag:\n%s", out)
	}
}

// TestBuildUserDataSnippet_TailscaleSkipsRoutesWhenPrefixMissing keeps the
// runcmd usable on legacy paths that haven't wired StaticPrivateIPPrefix —
// tailscale still joins the tailnet, just without route advertisement.
func TestBuildUserDataSnippet_TailscaleSkipsRoutesWhenPrefixMissing(t *testing.T) {
	t.Parallel()

	req := &cpi.InstanceRequest{
		Hostname:         "legacy-bastion",
		TailscaleAuthKey: "tskey-abc",
		StaticPrivateIP:  "10.4.0.3",
	}

	out := string(buildUserDataSnippet(req))

	if strings.Contains(out, "--advertise-routes") {
		t.Errorf("snippet should not advertise routes without prefix:\n%s", out)
	}

	if !strings.Contains(out, "tailscale up --authkey=") {
		t.Errorf("tailscale up should still run:\n%s", out)
	}
}

// TestDeriveAdvertiseRoutes exercises the CIDR-from-ip+prefix math directly
// so regressions surface independent of the snippet renderer.
func TestDeriveAdvertiseRoutes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		req    *cpi.InstanceRequest
		expect string
	}{
		{
			name:   "wayne vnet /18",
			req:    &cpi.InstanceRequest{StaticPrivateIP: "10.64.64.3", StaticPrivateIPPrefix: 18},
			expect: "10.64.64.0/18",
		},
		{
			name:   "norm vnet /18",
			req:    &cpi.InstanceRequest{StaticPrivateIP: "10.65.192.3", StaticPrivateIPPrefix: 18},
			expect: "10.65.192.0/18",
		},
		{
			name:   "embedded prefix tolerated",
			req:    &cpi.InstanceRequest{StaticPrivateIP: "10.64.64.3/24", StaticPrivateIPPrefix: 18},
			expect: "10.64.64.0/18",
		},
		{
			name:   "empty ip yields empty",
			req:    &cpi.InstanceRequest{StaticPrivateIPPrefix: 18},
			expect: "",
		},
		{
			name:   "zero prefix yields empty",
			req:    &cpi.InstanceRequest{StaticPrivateIP: "10.0.0.3"},
			expect: "",
		},
		{
			name:   "invalid prefix yields empty",
			req:    &cpi.InstanceRequest{StaticPrivateIP: "10.0.0.3", StaticPrivateIPPrefix: 99},
			expect: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := deriveAdvertiseRoutes(tt.req); got != tt.expect {
				t.Errorf("deriveAdvertiseRoutes() = %q, want %q", got, tt.expect)
			}
		})
	}
}

// TestSanitizeTailscaleAuthKey verifies shell-quote dangerous characters are
// stripped so the rendered `tailscale up --authkey="..."` line can never be
// broken out of by a malicious or accidentally-quoted auth key.
func TestSanitizeTailscaleAuthKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "clean", in: "tskey-abc123", want: "tskey-abc123"},
		{name: "strip double quotes", in: `tskey-"abc"`, want: "tskey-abc"},
		{name: "strip backslash", in: `tskey-\abc`, want: "tskey-abc"},
		{name: "strip newline", in: "tskey-\nabc", want: "tskey-abc"},
		{name: "strip backtick", in: "tskey-`abc`", want: "tskey-abc"},
		{name: "strip dollar", in: "tskey-$abc", want: "tskey-abc"},
		{name: "trim whitespace", in: "  tskey-abc  ", want: "tskey-abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := sanitizeTailscaleAuthKey(tt.in); got != tt.want {
				t.Errorf("sanitizeTailscaleAuthKey(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestGenerateDeterministicMAC pins three properties: (1) same input ->
// same MAC across runs, (2) locally-administered bit set on the first byte,
// (3) multicast bit cleared on the first byte. (3) is what keeps the address
// unicast-routable; (2) avoids stealing IEEE-assigned space.
func TestGenerateDeterministicMAC(t *testing.T) {
	t.Parallel()

	seed := "520-pve-wayne-bastion-100"

	a := generateDeterministicMAC(seed)
	b := generateDeterministicMAC(seed)

	if a != b {
		t.Errorf("MAC not deterministic: %s vs %s", a, b)
	}

	first := strings.Split(a, ":")[0]
	if len(first) != 2 {
		t.Fatalf("malformed first octet %q in %s", first, a)
	}

	var firstByte uint

	_, err := fmt.Sscanf(first, "%02x", &firstByte)
	if err != nil {
		t.Fatalf("hex parse: %v", err)
	}

	if firstByte&0x02 == 0 {
		t.Errorf("locally-administered bit not set in %s (first byte 0x%02x)", a, firstByte)
	}

	if firstByte&0x01 != 0 {
		t.Errorf("multicast bit set in %s (first byte 0x%02x)", a, firstByte)
	}
}

// TestBuildNetworkDataSnippet covers the cloud-init network-data v2 shape:
// MAC match block, address with CIDR prefix, gateway, DNS list, and search
// domain. v2 lets the snippet rename whatever NIC PVE creates to eth0, which
// is what makes the static IP land on the right device on Ubuntu 22.04+.
func TestBuildNetworkDataSnippet(t *testing.T) {
	t.Parallel()

	req := &cpi.InstanceRequest{
		StaticPrivateIP: "10.4.4.3/22",
		GatewayIP:       "10.4.4.1",
		DNSServers:      []string{"1.1.1.1", "8.8.8.8"},
		DomainSuffix:    "lab.fivetwenty.io",
	}

	mac := "02:ab:cd:ef:12:34"
	out := string(buildNetworkDataSnippet(req, mac))

	checks := []string{
		"version: 2",
		"ethernets:",
		"  primary:",
		"    match:",
		`      macaddress: "02:ab:cd:ef:12:34"`,
		"    set-name: eth0",
		"    addresses: [10.4.4.3/22]",
		"    gateway4: 10.4.4.1",
		"    nameservers:",
		"      addresses: [1.1.1.1, 8.8.8.8]",
		"      search: [lab.fivetwenty.io]",
	}

	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("snippet missing %q\n----\n%s", want, out)
		}
	}
}

// TestBuildNetworkDataSnippetDHCPSkipped ensures we don't emit a snippet for
// DHCP-only requests. Letting PVE's own v1 path generate the config is the
// right call when no static IP is requested.
func TestBuildNetworkDataSnippetDHCPSkipped(t *testing.T) {
	t.Parallel()

	out := buildNetworkDataSnippet(&cpi.InstanceRequest{StaticPrivateIP: ""}, "02:aa:bb:cc:dd:ee")
	if len(out) != 0 {
		t.Errorf("expected empty snippet for DHCP request, got %d bytes:\n%s", len(out), out)
	}
}

// TestBuildPVENet0WithMAC asserts the deterministic MAC is folded into net0.
// Without the MAC we lost determinism — auto-assigned MACs vary per clone
// retry and the network-data v2 match block stopped matching.
func TestBuildPVENet0WithMAC(t *testing.T) {
	t.Parallel()

	withMAC := buildPVENet0("vmbr1", "02:ab:cd:ef:12:34")
	want := "virtio=02:ab:cd:ef:12:34,bridge=vmbr1,firewall=1"

	if withMAC != want {
		t.Errorf("net0 = %q, want %q", withMAC, want)
	}

	noMAC := buildPVENet0("vmbr1", "")
	if noMAC != "virtio,bridge=vmbr1,firewall=1" {
		t.Errorf("no-MAC net0 = %q", noMAC)
	}
}

// TestBuildPVECICustom asserts the cicustom string carries both entries when
// the network snippet was uploaded, and omits the network entry when only
// user-data uploaded. Empty plan returns empty string so the caller drops
// the key entirely.
func TestBuildPVECICustom(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		plan cloudInitSnippetPlan
		want string
	}{
		{
			name: "no storage",
			plan: cloudInitSnippetPlan{},
			want: "",
		},
		{
			name: "user only",
			plan: cloudInitSnippetPlan{
				Storage:      "local",
				UserFilename: "bastion-100-user.yml",
			},
			want: "user=local:snippets/bastion-100-user.yml",
		},
		{
			name: "user + network",
			plan: cloudInitSnippetPlan{
				Storage:         "local",
				UserFilename:    "bastion-100-user.yml",
				NetworkFilename: "bastion-100-network.yml",
				HasNetwork:      true,
			},
			want: "user=local:snippets/bastion-100-user.yml,network=local:snippets/bastion-100-network.yml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := buildPVECICustom(tt.plan); got != tt.want {
				t.Errorf("cicustom = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestPickSnippetStorage covers the resolver precedence: ISOStorage override
// wins when snippet-capable; otherwise the first eligible entry wins. Disabled
// or non-snippet-capable entries are skipped.
func TestPickSnippetStorage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		entries    []nodeStorage
		isoStorage string
		want       string
	}{
		{
			name: "iso override wins when snippet-capable",
			entries: []nodeStorage{
				{name: "local", content: "iso,vztmpl,snippets", enabled: true},
				{name: "ceph", content: "snippets", enabled: true},
			},
			isoStorage: "ceph",
			want:       "ceph",
		},
		{
			name: "iso override falls back when not snippet-capable",
			entries: []nodeStorage{
				{name: "local-lvm", content: "images,rootdir", enabled: true},
				{name: "local", content: "iso,snippets,vztmpl", enabled: true},
			},
			isoStorage: "local-lvm",
			want:       "local",
		},
		{
			name: "first eligible wins when no override",
			entries: []nodeStorage{
				{name: "local-lvm", content: "images", enabled: true},
				{name: "local", content: "iso,vztmpl,snippets", enabled: true},
				{name: "cephfs", content: "snippets,backup", enabled: true},
			},
			want: "local",
		},
		{
			name: "disabled storage skipped",
			entries: []nodeStorage{
				{name: "local", content: "snippets", enabled: false},
				{name: "cephfs", content: "snippets", enabled: true},
			},
			want: "cephfs",
		},
		{
			name:    "no snippet-capable storage",
			entries: []nodeStorage{{name: "local-lvm", content: "images", enabled: true}},
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := pickSnippetStorage(tt.entries, tt.isoStorage); got != tt.want {
				t.Errorf("pickSnippetStorage() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestBuildPVEDirectCloudInitConfigWithSnippetPlan asserts the cicustom and
// net0-with-MAC are folded into the direct config map when a snippet plan
// is present. Mirrors the original TestBuildPVEDirectCloudInitConfig but
// covers the new wiring.
func TestBuildPVEDirectCloudInitConfigWithSnippetPlan(t *testing.T) {
	t.Parallel()

	req := &cpi.InstanceRequest{
		NetworkID:       "vmbr1",
		StaticPrivateIP: "10.4.4.3/22",
		GatewayIP:       "10.4.4.1",
		DefaultUsername: "ubuntu",
	}

	plan := cloudInitSnippetPlan{
		Storage:         "local",
		UserFilename:    "520-pve-wayne-bastion-100-user.yml",
		NetworkFilename: "520-pve-wayne-bastion-100-network.yml",
		MAC:             "02:ab:cd:ef:12:34",
		HasNetwork:      true,
	}

	got := buildPVEDirectCloudInitConfig(req, plan)

	if got["net0"] != "virtio=02:ab:cd:ef:12:34,bridge=vmbr1,firewall=1" {
		t.Errorf("net0 = %v", got["net0"])
	}

	wantCicustom := "user=local:snippets/520-pve-wayne-bastion-100-user.yml,network=local:snippets/520-pve-wayne-bastion-100-network.yml"
	if got["cicustom"] != wantCicustom {
		t.Errorf("cicustom = %v\nwant: %s", got["cicustom"], wantCicustom)
	}
}

// TestBuildPVEDirectCloudInitConfigNoSnippetPlanOmitsCICustom guards against
// emitting cicustom when no snippets uploaded. PVE rejects malformed cicustom
// strings, so we must not put a key with empty values into the PUT body.
func TestBuildPVEDirectCloudInitConfigNoSnippetPlanOmitsCICustom(t *testing.T) {
	t.Parallel()

	got := buildPVEDirectCloudInitConfig(&cpi.InstanceRequest{NetworkID: "vmbr1"}, cloudInitSnippetPlan{})

	if _, exists := got["cicustom"]; exists {
		t.Errorf("cicustom should be absent for empty plan, got %v", got["cicustom"])
	}
}
