package pve

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

// TestSanitizePVEGroupName checks that the helper passes through identifiers
// already legal under PVE's group regex and otherwise produces a deterministic,
// regex-compliant fallback that is short enough for the 18-char limit.
func TestSanitizePVEGroupName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantPass  bool   // expect identity passthrough
		wantExact string // when wantPass=false, exact expected sanitized form
	}{
		{
			name:     "valid short name passes through",
			input:    "bastion",
			wantPass: true,
		},
		{
			name:     "valid 18-char name passes through",
			input:    "abcdefghij12345678", // 18 chars, starts with letter
			wantPass: true,
		},
		{
			name:     "valid with dash and underscore",
			input:    "ocfp-sg_1",
			wantPass: true,
		},
		{
			name:     "leading digit is sanitized",
			input:    "520-pve-wayne-bastion",
			wantPass: false,
		},
		{
			name:     "too long is sanitized",
			input:    "this-is-a-very-long-bloc-name-bastion",
			wantPass: false,
		},
		{
			name:     "contains dot is sanitized",
			input:    "bloc.bastion",
			wantPass: false,
		},
		{
			name:     "single char is sanitized (regex requires 2-18)",
			input:    "x",
			wantPass: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitizePVEGroupName(tt.input)

			if tt.wantPass {
				if got != tt.input {
					t.Errorf("sanitize(%q) = %q, want passthrough (%q)", tt.input, got, tt.input)
				}

				return
			}

			if got == tt.input {
				t.Errorf("sanitize(%q) returned input unchanged, expected hashed form", tt.input)
			}

			if !pveGroupNameRegex.MatchString(got) {
				t.Errorf("sanitize(%q) = %q does not satisfy PVE group regex", tt.input, got)
			}

			// Hash form is "g-XXXXXXXX" — 10 chars total.
			const expectLen = 10
			if len(got) != expectLen {
				t.Errorf("sanitize(%q) = %q length = %d, want %d", tt.input, got, len(got), expectLen)
			}

			// Stable across calls.
			if again := sanitizePVEGroupName(tt.input); again != got {
				t.Errorf("sanitize(%q) is not deterministic: got %q then %q", tt.input, got, again)
			}
		})
	}
}

// TestSanitizePVEGroupName_DistinctInputsProduceDistinctOutputs guards
// against accidental collisions for the small handful of OCFP names that
// realistically need sanitizing (bloc prefixes + role suffixes).
func TestSanitizePVEGroupName_DistinctInputsProduceDistinctOutputs(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"520-pve-wayne-bastion",
		"520-pve-wayne-bosh",
		"520-pve-wayne-vault",
		"520-pve-wayne-jumpbox",
		"520-pve-wayne-shield",
	}

	seen := make(map[string]string, len(inputs))

	for _, in := range inputs {
		out := sanitizePVEGroupName(in)
		if prev, dup := seen[out]; dup {
			t.Errorf("collision: sanitize(%q) and sanitize(%q) both = %q", in, prev, out)
		}

		seen[out] = in
	}
}

// TestExtractOCFPNameFromComment confirms that the reverse-lookup helper used
// by ListSecurityGroups recovers the original OCFP-side name from the two
// comment formats CreateSecurityGroup writes.
func TestExtractOCFPNameFromComment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		comment string
		want    string
	}{
		{
			name:    "bare marker",
			comment: "ocfp:520-pve-wayne-bastion",
			want:    "520-pve-wayne-bastion",
		},
		{
			name:    "description with marker in parens (legacy format)",
			comment: "Bastion SSH ingress (ocfp:520-pve-wayne-bastion)",
			want:    "520-pve-wayne-bastion",
		},
		{
			name:    "marker prefix with dash separator (current format)",
			comment: "ocfp:520-pve-wayne-bastion - Bastion SSH ingress",
			want:    "520-pve-wayne-bastion",
		},
		{
			name:    "no marker present",
			comment: "Manually created by operator",
			want:    "",
		},
		{
			name:    "empty comment",
			comment: "",
			want:    "",
		},
		{
			name:    "marker is case-sensitive",
			comment: "OCFP:520-pve-wayne-bastion",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := extractOCFPNameFromComment(tt.comment)
			if got != tt.want {
				t.Errorf("extractOCFPNameFromComment(%q) = %q, want %q", tt.comment, got, tt.want)
			}
		})
	}
}

// TestFormatPVETag covers the key=value → PVE-identifier mapping the bastion
// label-discovery path relies on. PVE's tag charset is restrictive; every
// other byte must be collapsed to "_" so the API doesn't reject the PUT.
func TestFormatPVETag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		key   string
		value string
		want  string
	}{
		{name: "bloc name", key: "bloc", value: "520-pve-wayne", want: "bloc--520-pve-wayne"},
		{name: "role", key: "role", value: "bastion", want: "role--bastion"},
		{name: "uppercase is lowered", key: "Role", value: "Bastion", want: "role--bastion"},
		{name: "slashes replaced", key: "managed-by", value: "ocfp/cli", want: "managed-by--ocfp_cli"},
		{name: "spaces replaced", key: "env", value: "prod east", want: "env--prod_east"},
		{name: "dots preserved", key: "version", value: "1.2.3", want: "version--1.2.3"},
		{name: "empty value still tags key", key: "managed-by", value: "", want: "managed-by--"},
		{name: "both empty drops tag", key: "", value: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := formatPVETag(tt.key, tt.value)
			if got != tt.want {
				t.Errorf("formatPVETag(%q, %q) = %q, want %q", tt.key, tt.value, got, tt.want)
			}
		})
	}
}

// TestParsePVETags handles the inverse direction — splitting PVE's
// semicolon-joined string back into individual tags, dropping empty entries
// from leading/trailing/duplicate separators.
func TestParsePVETags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "empty", raw: "", want: nil},
		{name: "single", raw: "bloc--prod", want: []string{"bloc--prod"}},
		{name: "two", raw: "bloc--prod;role--bastion", want: []string{"bloc--prod", "role--bastion"}},
		{name: "trailing separator", raw: "bloc--prod;", want: []string{"bloc--prod"}},
		{name: "duplicate separators", raw: ";bloc--prod;;role--bastion;", want: []string{"bloc--prod", "role--bastion"}},
		{name: "whitespace trimmed", raw: " bloc--prod ; role--bastion ", want: []string{"bloc--prod", "role--bastion"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := parsePVETags(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("parsePVETags(%q) = %v, want %v", tt.raw, got, tt.want)
			}

			for i, g := range got {
				if g != tt.want[i] {
					t.Errorf("parsePVETags(%q)[%d] = %q, want %q", tt.raw, i, g, tt.want[i])
				}
			}
		})
	}
}

// TestMatchesLabelFilters checks the predicate that drives label-based VM
// discovery — ListInstances applies it to skip VMs whose tag set doesn't
// satisfy the caller's "label.*" filters.
func TestMatchesLabelFilters(t *testing.T) {
	t.Parallel()

	tags := []string{"bloc--520-pve-wayne", "role--bastion", "managed-by--ocfp"}

	tests := []struct {
		name    string
		vmTags  []string
		filters map[string]string
		want    bool
	}{
		{
			name:    "no filters matches anything",
			vmTags:  tags,
			filters: nil,
			want:    true,
		},
		{
			name:    "single label match",
			vmTags:  tags,
			filters: map[string]string{"label.bloc": "520-pve-wayne"},
			want:    true,
		},
		{
			name:    "multiple labels all match",
			vmTags:  tags,
			filters: map[string]string{"label.bloc": "520-pve-wayne", "label.role": "bastion"},
			want:    true,
		},
		{
			name:    "missing label rejects",
			vmTags:  tags,
			filters: map[string]string{"label.bloc": "other-bloc"},
			want:    false,
		},
		{
			name:    "non-label filters ignored",
			vmTags:  tags,
			filters: map[string]string{"name": "anything"},
			want:    true,
		},
		{
			name:    "untagged VM fails any label filter",
			vmTags:  nil,
			filters: map[string]string{"label.bloc": "520-pve-wayne"},
			want:    false,
		},
		{
			name:    "casing differences still match (key/value lowered)",
			vmTags:  tags,
			filters: map[string]string{"label.Bloc": "520-PVE-Wayne"},
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := matchesLabelFilters(tt.vmTags, tt.filters)
			if got != tt.want {
				t.Errorf("matchesLabelFilters(%v, %v) = %v, want %v", tt.vmTags, tt.filters, got, tt.want)
			}
		})
	}
}

// TestExtractIPConfigAddress covers the cloud-init ipconfig0 parser used as
// a fallback when QGA hasn't reported IPs yet.
func TestExtractIPConfigAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ipconfig string
		want     string
	}{
		{name: "empty", ipconfig: "", want: ""},
		{name: "dhcp", ipconfig: "ip=dhcp", want: ""},
		{name: "static cidr", ipconfig: "ip=192.168.1.67/24,gw=192.168.1.1", want: "192.168.1.67"},
		{name: "static no cidr", ipconfig: "ip=10.0.0.5,gw=10.0.0.1", want: "10.0.0.5"},
		{name: "with ip6", ipconfig: "ip=192.168.1.67/24,gw=192.168.1.1,ip6=auto", want: "192.168.1.67"},
		{name: "no ip field", ipconfig: "gw=192.168.1.1", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := extractIPConfigAddress(tt.ipconfig)
			if got != tt.want {
				t.Errorf("extractIPConfigAddress(%q) = %q, want %q", tt.ipconfig, got, tt.want)
			}
		})
	}
}

// TestExtractAgentPrimaryIPv4 covers the QGA network-get-interfaces parser
// used to resolve a running VM's primary address when ipconfig0 is DHCP and
// status/current has no network data.
func TestExtractAgentPrimaryIPv4(t *testing.T) {
	t.Parallel()

	ipv4 := func(addr string) map[string]interface{} {
		return map[string]interface{}{"ip-address": addr, "ip-address-type": "ipv4"}
	}

	ipv6 := func(addr string) map[string]interface{} {
		return map[string]interface{}{"ip-address": addr, "ip-address-type": "ipv6"}
	}

	tests := []struct {
		name string
		resp interface{}
		want string
	}{
		{name: "nil response", resp: nil, want: ""},
		{
			name: "result wrapper with primary nic",
			resp: map[string]interface{}{
				"result": []interface{}{
					map[string]interface{}{
						"name":         "lo",
						"ip-addresses": []interface{}{ipv4("127.0.0.1")},
					},
					map[string]interface{}{
						"name":         "ens18",
						"ip-addresses": []interface{}{ipv6("fe80::1"), ipv4("10.4.4.3")},
					},
				},
			},
			want: "10.4.4.3",
		},
		{
			name: "bare array response",
			resp: []interface{}{
				map[string]interface{}{
					"name":         "ens18",
					"ip-addresses": []interface{}{ipv4("192.168.1.67")},
				},
			},
			want: "192.168.1.67",
		},
		{
			name: "skip link-local and loopback",
			resp: map[string]interface{}{
				"result": []interface{}{
					map[string]interface{}{
						"name":         "ens18",
						"ip-addresses": []interface{}{ipv4("169.254.10.20"), ipv4("127.0.0.5"), ipv4("10.4.4.3")},
					},
				},
			},
			want: "10.4.4.3",
		},
		{
			name: "no ipv4 anywhere",
			resp: map[string]interface{}{
				"result": []interface{}{
					map[string]interface{}{
						"name":         "ens18",
						"ip-addresses": []interface{}{ipv6("fe80::1")},
					},
				},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := extractAgentPrimaryIPv4(tt.resp)
			if got != tt.want {
				t.Errorf("extractAgentPrimaryIPv4(%v) = %q, want %q", tt.resp, got, tt.want)
			}
		})
	}
}

// TestBuildPVEIPConfig covers the ipconfig0 builder that determines whether
// the bastion gets DHCP or a static address with gateway.
func TestBuildPVEIPConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		req  *cpi.InstanceRequest
		want string
	}{
		{
			name: "no static ip → dhcp",
			req:  &cpi.InstanceRequest{},
			want: "ip=dhcp",
		},
		{
			name: "static ip without prefix gets /24",
			req:  &cpi.InstanceRequest{StaticPrivateIP: "192.168.1.67"},
			want: "ip=192.168.1.67/24",
		},
		{
			name: "static ip with explicit prefix preserved",
			req:  &cpi.InstanceRequest{StaticPrivateIP: "10.0.0.5/26"},
			want: "ip=10.0.0.5/26",
		},
		{
			name: "static ip with gateway",
			req:  &cpi.InstanceRequest{StaticPrivateIP: "192.168.1.67", GatewayIP: "192.168.1.1"},
			want: "ip=192.168.1.67/24,gw=192.168.1.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := buildPVEIPConfig(tt.req)
			if got != tt.want {
				t.Errorf("buildPVEIPConfig(%+v) = %q, want %q", tt.req, got, tt.want)
			}
		})
	}
}

// TestBuildPVEDirectCloudInitConfig confirms the cloud-init PUT body has
// every load-bearing key set when the operator provides full bastion
// details, and that the sshkeys field is URL-encoded as PVE requires.
func TestBuildPVEDirectCloudInitConfig(t *testing.T) {
	t.Parallel()

	const samplePubKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5xxxxxx ocfp-test"

	req := &cpi.InstanceRequest{
		NetworkID:       "vmbr1",
		StaticPrivateIP: "192.168.1.67",
		GatewayIP:       "192.168.1.1",
		DNSServers:      []string{"1.1.1.1", "8.8.8.8"},
		DefaultUsername: "ubuntu",
		PublicKey:       samplePubKey,
	}

	got := buildPVEDirectCloudInitConfig(req, cloudInitSnippetPlan{})

	if got["net0"] != "virtio,bridge=vmbr1,firewall=1" {
		t.Errorf("net0 = %v", got["net0"])
	}

	if got["ipconfig0"] != "ip=192.168.1.67/24,gw=192.168.1.1" {
		t.Errorf("ipconfig0 = %v", got["ipconfig0"])
	}

	if got["nameserver"] != "1.1.1.1 8.8.8.8" {
		t.Errorf("nameserver = %v", got["nameserver"])
	}

	if got["ciuser"] != "ubuntu" {
		t.Errorf("ciuser = %v", got["ciuser"])
	}

	rawSSH, ok := got["sshkeys"].(string)
	if !ok {
		t.Fatalf("sshkeys missing or wrong type: %T", got["sshkeys"])
	}

	// PVE rejects "+" as the form-urlencoded space character — only the
	// percent-encoded form (%20) is accepted. Guard against regression.
	if strings.Contains(rawSSH, "+") {
		t.Errorf("sshkeys contains '+' (form-urlencoded space); PVE requires %%20: %q", rawSSH)
	}

	// Round-trip back to the original (+ trailing newline).
	decoded, err := url.QueryUnescape(rawSSH)
	if err != nil {
		t.Fatalf("sshkeys not URL-decodable: %v", err)
	}

	if strings.TrimRight(decoded, "\n") != samplePubKey {
		t.Errorf("sshkeys round-trip = %q, want %q (with trailing newline)", decoded, samplePubKey)
	}
}

// TestPveEncodeSSHKeys_NoPlus pins the encoding rule: spaces in OpenSSH
// public key lines must come out as %20, never as the +-form. This is the
// single character that differs between net/url's QueryEscape and what PVE
// accepts on the sshkeys parameter.
func TestPveEncodeSSHKeys_NoPlus(t *testing.T) {
	t.Parallel()

	encoded := pveEncodeSSHKeys("ssh-ed25519 ABC ocfp")

	if strings.Contains(encoded, "+") {
		t.Errorf("encoded sshkeys contains '+': %q", encoded)
	}

	if !strings.Contains(encoded, "%20") {
		t.Errorf("encoded sshkeys missing %%20 separators: %q", encoded)
	}
}

// TestBuildPVEDirectCloudInitConfig_EmptyRequest verifies the helper
// produces nothing for an InstanceRequest with no cloud-init fields set
// — letting the caller skip the PUT entirely on plain VMs.
func TestBuildPVEDirectCloudInitConfig_EmptyRequest(t *testing.T) {
	t.Parallel()

	got := buildPVEDirectCloudInitConfig(&cpi.InstanceRequest{}, cloudInitSnippetPlan{})

	// Even an empty request produces ipconfig0=ip=dhcp by design — DHCP is
	// the safe default for any VM lacking explicit network config. So we
	// expect exactly one key.
	if len(got) != 1 {
		t.Fatalf("expected 1 key in default config, got %d: %v", len(got), got)
	}

	if got["ipconfig0"] != "ip=dhcp" {
		t.Errorf("ipconfig0 default = %v, want ip=dhcp", got["ipconfig0"])
	}
}

// TestPveGroupNameRegexShape is a guard against accidental edits to the
// regex literal — the limits encoded here are the load-bearing PVE constraint.
func TestPveGroupNameRegexShape(t *testing.T) {
	t.Parallel()

	expect := regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{1,17}$`)
	if pveGroupNameRegex.String() != expect.String() {
		t.Errorf("pveGroupNameRegex = %q, want %q", pveGroupNameRegex.String(), expect.String())
	}

	if pveGroupNameMaxLen != 18 {
		t.Errorf("pveGroupNameMaxLen = %d, want 18", pveGroupNameMaxLen)
	}
}
