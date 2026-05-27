package aws

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

func nilSecurityManager() *SecurityManager {
	return &SecurityManager{client: nil}
}

// ---- validateDirection ------------------------------------------------------

func TestValidateDirection(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	valid := []string{"ingress", "egress", "inbound", "outbound", "INGRESS", "EGRESS"}
	for _, dir := range valid {
		if err := m.validateDirection(dir); err != nil {
			t.Errorf("validateDirection(%q) unexpected error: %v", dir, err)
		}
	}

	invalid := []string{"", "in", "out", "forward", "both"}
	for _, dir := range invalid {
		if err := m.validateDirection(dir); err == nil {
			t.Errorf("validateDirection(%q) expected error, got nil", dir)
		}
	}
}

// ---- validateProtocol -------------------------------------------------------

func TestValidateProtocol(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	valid := []string{"tcp", "udp", "icmp", "icmpv6", "all", "", "TCP", "0", "17", "255"}
	for _, proto := range valid {
		if err := m.validateProtocol(proto); err != nil {
			t.Errorf("validateProtocol(%q) unexpected error: %v", proto, err)
		}
	}

	invalid := []string{"256", "-1", "ftp", "ospf"}
	for _, proto := range invalid {
		if err := m.validateProtocol(proto); err == nil {
			t.Errorf("validateProtocol(%q) expected error, got nil", proto)
		}
	}
}

// ---- validatePorts ----------------------------------------------------------

func TestValidatePorts(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()

	validCases := []*cpi.SecurityRule{
		{PortRangeMin: 0, PortRangeMax: 0},
		{PortRangeMin: 80, PortRangeMax: 80},
		{PortRangeMin: 1, PortRangeMax: 65535},
		{PortRangeMin: 0, PortRangeMax: 65535},
	}
	for _, rule := range validCases {
		if err := m.validatePorts(rule); err != nil {
			t.Errorf("validatePorts(%+v) unexpected error: %v", rule, err)
		}
	}

	invalidCases := []*cpi.SecurityRule{
		{PortRangeMin: -1, PortRangeMax: 80},
		{PortRangeMin: 80, PortRangeMax: 70},     // min > max
		{PortRangeMin: 80, PortRangeMax: 65536},
	}
	for _, rule := range invalidCases {
		if err := m.validatePorts(rule); err == nil {
			t.Errorf("validatePorts(%+v) expected error, got nil", rule)
		}
	}
}

// ---- validateRemote ---------------------------------------------------------

func TestValidateRemote(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()

	if err := m.validateRemote(&cpi.SecurityRule{RemoteIPCIDR: "10.0.0.0/8"}); err != nil {
		t.Errorf("valid CIDR: unexpected error: %v", err)
	}
	if err := m.validateRemote(&cpi.SecurityRule{RemoteGroup: "sg-abc123"}); err != nil {
		t.Errorf("valid sg ref: unexpected error: %v", err)
	}

	// both empty
	if err := m.validateRemote(&cpi.SecurityRule{}); err == nil {
		t.Errorf("both empty: expected error, got nil")
	}
	// both set
	if err := m.validateRemote(&cpi.SecurityRule{RemoteIPCIDR: "10.0.0.0/8", RemoteGroup: "sg-abc"}); err == nil {
		t.Errorf("both set: expected error, got nil")
	}
	// invalid CIDR
	if err := m.validateRemote(&cpi.SecurityRule{RemoteIPCIDR: "not-a-cidr"}); err == nil {
		t.Errorf("invalid CIDR: expected error, got nil")
	}
	// sg without sg- prefix
	if err := m.validateRemote(&cpi.SecurityRule{RemoteGroup: "abc-123"}); err == nil {
		t.Errorf("bad sg prefix: expected error, got nil")
	}
}

// ---- generateRuleID ---------------------------------------------------------

func TestGenerateRuleID(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	id := m.generateRuleID("ingress", "tcp", 80, 80, "10.0.0.0/8")
	want := "ingress:tcp:80:80:10.0.0.0/8"
	if id != want {
		t.Errorf("generateRuleID = %q, want %q", id, want)
	}
}

// ---- ipPermissionToRules ----------------------------------------------------

func TestIpPermissionToRules_IPv4CIDR(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	fromPort := int32(443)
	toPort := int32(443)
	perm := &ec2types.IpPermission{
		IpProtocol: aws.String("tcp"),
		FromPort:   &fromPort,
		ToPort:     &toPort,
		IpRanges: []ec2types.IpRange{
			{CidrIp: aws.String("0.0.0.0/0"), Description: aws.String("all traffic")},
		},
	}

	rules := m.ipPermissionToRules("ingress", perm)

	if len(rules) != 1 {
		t.Fatalf("len(rules) = %d, want 1", len(rules))
	}
	r := rules[0]
	if r.Protocol != "tcp" {
		t.Errorf("Protocol = %q, want tcp", r.Protocol)
	}
	if r.PortRangeMin != 443 || r.PortRangeMax != 443 {
		t.Errorf("Ports = %d-%d, want 443-443", r.PortRangeMin, r.PortRangeMax)
	}
	if r.RemoteIPCIDR != "0.0.0.0/0" {
		t.Errorf("RemoteIPCIDR = %q, want 0.0.0.0/0", r.RemoteIPCIDR)
	}
	if r.Direction != "ingress" {
		t.Errorf("Direction = %q, want ingress", r.Direction)
	}
}

func TestIpPermissionToRules_AllProtocol(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	perm := &ec2types.IpPermission{
		IpProtocol: aws.String("-1"),
		IpRanges: []ec2types.IpRange{
			{CidrIp: aws.String("10.0.0.0/8")},
		},
	}

	rules := m.ipPermissionToRules("egress", perm)

	if len(rules) != 1 {
		t.Fatalf("len(rules) = %d, want 1", len(rules))
	}
	if rules[0].Protocol != "all" {
		t.Errorf("Protocol = %q, want all", rules[0].Protocol)
	}
}

func TestIpPermissionToRules_ICMP(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	fromPort := int32(8)
	toPort := int32(0)
	perm := &ec2types.IpPermission{
		IpProtocol: aws.String("icmp"),
		FromPort:   &fromPort,
		ToPort:     &toPort,
		IpRanges: []ec2types.IpRange{
			{CidrIp: aws.String("10.0.0.0/8")},
		},
	}

	rules := m.ipPermissionToRules("ingress", perm)

	if len(rules) != 1 {
		t.Fatalf("len(rules) = %d, want 1", len(rules))
	}
	// ICMP ports zeroed out
	if rules[0].PortRangeMin != 0 || rules[0].PortRangeMax != 0 {
		t.Errorf("ICMP ports = %d-%d, want 0-0", rules[0].PortRangeMin, rules[0].PortRangeMax)
	}
}

func TestIpPermissionToRules_SecurityGroupRef(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	fromPort := int32(8080)
	toPort := int32(8080)
	perm := &ec2types.IpPermission{
		IpProtocol: aws.String("tcp"),
		FromPort:   &fromPort,
		ToPort:     &toPort,
		UserIdGroupPairs: []ec2types.UserIdGroupPair{
			{GroupId: aws.String("sg-ref123"), Description: aws.String("internal")},
		},
	}

	rules := m.ipPermissionToRules("ingress", perm)

	if len(rules) != 1 {
		t.Fatalf("len(rules) = %d, want 1", len(rules))
	}
	if rules[0].RemoteGroup != "sg-ref123" {
		t.Errorf("RemoteGroup = %q, want sg-ref123", rules[0].RemoteGroup)
	}
	if rules[0].RemoteIPCIDR != "" {
		t.Errorf("RemoteIPCIDR should be empty for SG ref rule, got %q", rules[0].RemoteIPCIDR)
	}
}

func TestIpPermissionToRules_IPv6CIDR(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	perm := &ec2types.IpPermission{
		IpProtocol: aws.String("tcp"),
		Ipv6Ranges: []ec2types.Ipv6Range{
			{CidrIpv6: aws.String("::/0")},
		},
	}

	rules := m.ipPermissionToRules("ingress", perm)

	if len(rules) != 1 {
		t.Fatalf("len(rules) = %d, want 1", len(rules))
	}
	if rules[0].RemoteIPCIDR != "::/0" {
		t.Errorf("RemoteIPCIDR = %q, want ::/0", rules[0].RemoteIPCIDR)
	}
}

func TestIpPermissionToRules_Empty(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	perm := &ec2types.IpPermission{
		IpProtocol: aws.String("tcp"),
	}

	rules := m.ipPermissionToRules("ingress", perm)
	if len(rules) != 0 {
		t.Errorf("len(rules) = %d, want 0 (no ranges/groups)", len(rules))
	}
}

// ---- convertSecurityGroup ---------------------------------------------------

func TestConvertSecurityGroup_Basic(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	sg := &ec2types.SecurityGroup{
		GroupId:     aws.String("sg-basic"),
		GroupName:   aws.String("test-sg"),
		Description: aws.String("Test security group"),
		VpcId:       aws.String("vpc-abc"),
		Tags: []ec2types.Tag{
			{Key: aws.String("env"), Value: aws.String("dev")},
		},
	}

	got := m.convertSecurityGroup(sg)

	if got.ID != "sg-basic" {
		t.Errorf("ID = %q, want %q", got.ID, "sg-basic")
	}
	if got.Name != "test-sg" {
		t.Errorf("Name = %q, want %q", got.Name, "test-sg")
	}
	if got.Description != "Test security group" {
		t.Errorf("Description = %q, want %q", got.Description, "Test security group")
	}
	if got.NetworkID != "vpc-abc" {
		t.Errorf("NetworkID = %q, want %q", got.NetworkID, "vpc-abc")
	}
	if got.Tags["env"] != "dev" {
		t.Errorf("Tags[env] = %q, want %q", got.Tags["env"], "dev")
	}
}

func TestConvertSecurityGroup_WithRules(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	from := int32(80)
	to := int32(80)
	sg := &ec2types.SecurityGroup{
		GroupId:     aws.String("sg-rules"),
		GroupName:   aws.String("test"),
		Description: aws.String("desc"),
		VpcId:       aws.String("vpc-abc"),
		IpPermissions: []ec2types.IpPermission{
			{
				IpProtocol: aws.String("tcp"),
				FromPort:   &from,
				ToPort:     &to,
				IpRanges:   []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
			},
		},
		IpPermissionsEgress: []ec2types.IpPermission{
			{
				IpProtocol: aws.String("-1"),
				IpRanges:   []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
			},
		},
	}

	got := m.convertSecurityGroup(sg)

	if len(got.Rules) != 2 {
		t.Errorf("Rules len = %d, want 2 (1 ingress + 1 egress)", len(got.Rules))
	}
}

func TestConvertSecurityGroup_NilTagKeyValue(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	sg := &ec2types.SecurityGroup{
		GroupId: aws.String("sg-nil"),
		Tags: []ec2types.Tag{
			{Key: nil, Value: aws.String("val")},
			{Key: aws.String("k"), Value: nil},
		},
	}

	got := m.convertSecurityGroup(sg)
	// nil key or nil value tags must be skipped — map must be empty
	if len(got.Tags) != 0 {
		t.Errorf("Tags len = %d, want 0 (nil key/value tags skipped)", len(got.Tags))
	}
}

// ---- setProtocol ------------------------------------------------------------

func TestSetProtocol(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	tests := []struct {
		protocol string
		want     string
	}{
		{"tcp", "tcp"},
		{"UDP", "udp"},
		{"all", "-1"},
		{"", "-1"},
	}

	for _, tt := range tests {
		t.Run(tt.protocol, func(t *testing.T) {
			t.Parallel()
			perm := &ec2types.IpPermission{}
			m.setProtocol(perm, &cpi.SecurityRule{Protocol: tt.protocol})
			if aws.ToString(perm.IpProtocol) != tt.want {
				t.Errorf("IpProtocol = %q, want %q", aws.ToString(perm.IpProtocol), tt.want)
			}
		})
	}
}

// ---- setRemote --------------------------------------------------------------

func TestSetRemote_IPv4(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	perm := &ec2types.IpPermission{}
	err := m.setRemote(perm, &cpi.SecurityRule{RemoteIPCIDR: "10.0.0.0/8", Description: "desc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(perm.IpRanges) != 1 {
		t.Fatalf("IpRanges len = %d, want 1", len(perm.IpRanges))
	}
	if aws.ToString(perm.IpRanges[0].CidrIp) != "10.0.0.0/8" {
		t.Errorf("CidrIp = %q, want 10.0.0.0/8", aws.ToString(perm.IpRanges[0].CidrIp))
	}
}

func TestSetRemote_IPv6(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	perm := &ec2types.IpPermission{}
	err := m.setRemote(perm, &cpi.SecurityRule{RemoteIPCIDR: "::/0"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(perm.Ipv6Ranges) != 1 {
		t.Fatalf("Ipv6Ranges len = %d, want 1", len(perm.Ipv6Ranges))
	}
}

func TestSetRemote_SecurityGroup(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	perm := &ec2types.IpPermission{}
	err := m.setRemote(perm, &cpi.SecurityRule{RemoteGroup: "sg-abc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(perm.UserIdGroupPairs) != 1 {
		t.Fatalf("UserIdGroupPairs len = %d, want 1", len(perm.UserIdGroupPairs))
	}
}

// ---- setPorts ---------------------------------------------------------------

func TestSetPorts(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()

	tests := []struct {
		name     string
		protocol string
		min, max int
		wantFrom *int32
		wantTo   *int32
	}{
		{
			name: "both ports set",
			protocol: "tcp", min: 80, max: 443,
			wantFrom: aws.Int32(80), wantTo: aws.Int32(443),
		},
		{
			name: "only min set — to mirrors from",
			protocol: "tcp", min: 22, max: 0,
			wantFrom: aws.Int32(22), wantTo: aws.Int32(22),
		},
		{
			name: "only max set — from mirrors to",
			protocol: "tcp", min: 0, max: 8080,
			wantFrom: aws.Int32(8080), wantTo: aws.Int32(8080),
		},
		{
			name: "both zero",
			protocol: "tcp", min: 0, max: 0,
			wantFrom: nil, wantTo: nil,
		},
		{
			name: "icmp — no ports set even when provided",
			protocol: "icmp", min: 8, max: 0,
			wantFrom: nil, wantTo: nil,
		},
		{
			name: "all — no ports set",
			protocol: "all", min: 80, max: 80,
			wantFrom: nil, wantTo: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			perm := &ec2types.IpPermission{}
			rule := &cpi.SecurityRule{Protocol: tt.protocol, PortRangeMin: tt.min, PortRangeMax: tt.max}
			m.setPorts(perm, rule)

			fromMatch := (perm.FromPort == nil) == (tt.wantFrom == nil)
			if fromMatch && tt.wantFrom != nil {
				fromMatch = aws.ToInt32(perm.FromPort) == aws.ToInt32(tt.wantFrom)
			}
			if !fromMatch {
				t.Errorf("FromPort = %v, want %v", perm.FromPort, tt.wantFrom)
			}

			toMatch := (perm.ToPort == nil) == (tt.wantTo == nil)
			if toMatch && tt.wantTo != nil {
				toMatch = aws.ToInt32(perm.ToPort) == aws.ToInt32(tt.wantTo)
			}
			if !toMatch {
				t.Errorf("ToPort = %v, want %v", perm.ToPort, tt.wantTo)
			}
		})
	}
}

// ---- validateSecurityRule ---------------------------------------------------

func TestValidateSecurityRule_Valid(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	rule := &cpi.SecurityRule{
		Direction:    "ingress",
		Protocol:     "tcp",
		PortRangeMin: 80,
		PortRangeMax: 80,
		RemoteIPCIDR: "10.0.0.0/8",
	}
	if err := m.validateSecurityRule(rule); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidateSecurityRule_InvalidDirection(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	rule := &cpi.SecurityRule{Direction: "forward", Protocol: "tcp", RemoteIPCIDR: "0.0.0.0/0"}
	if err := m.validateSecurityRule(rule); err == nil {
		t.Errorf("expected error for invalid direction, got nil")
	}
}

func TestValidateSecurityRule_InvalidProtocol(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	rule := &cpi.SecurityRule{Direction: "ingress", Protocol: "ftp", RemoteIPCIDR: "0.0.0.0/0"}
	if err := m.validateSecurityRule(rule); err == nil {
		t.Errorf("expected error for invalid protocol, got nil")
	}
}

func TestValidateSecurityRule_InvalidPorts(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	rule := &cpi.SecurityRule{Direction: "ingress", Protocol: "tcp", PortRangeMin: 500, PortRangeMax: 100, RemoteIPCIDR: "0.0.0.0/0"}
	if err := m.validateSecurityRule(rule); err == nil {
		t.Errorf("expected error for min > max, got nil")
	}
}

func TestValidateSecurityRule_InvalidRemote(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	rule := &cpi.SecurityRule{Direction: "ingress", Protocol: "tcp", RemoteIPCIDR: "", RemoteGroup: ""}
	if err := m.validateSecurityRule(rule); err == nil {
		t.Errorf("expected error for empty remote, got nil")
	}
}

// ---- ruleToIPPermission -----------------------------------------------------

func TestRuleToIPPermission_TCPWithCIDR(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	rule := &cpi.SecurityRule{
		Protocol:     "tcp",
		PortRangeMin: 443,
		PortRangeMax: 443,
		RemoteIPCIDR: "0.0.0.0/0",
	}

	perm, err := m.ruleToIPPermission(rule)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if aws.ToString(perm.IpProtocol) != "tcp" {
		t.Errorf("IpProtocol = %q, want tcp", aws.ToString(perm.IpProtocol))
	}
	if len(perm.IpRanges) != 1 {
		t.Errorf("IpRanges len = %d, want 1", len(perm.IpRanges))
	}
}

func TestRuleToIPPermission_NoRemote(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	rule := &cpi.SecurityRule{Protocol: "tcp", PortRangeMin: 80, PortRangeMax: 80}
	_, err := m.ruleToIPPermission(rule)
	if err == nil {
		t.Errorf("expected error for empty remote, got nil")
	}
}

// ---- prepareTags ------------------------------------------------------------

func TestPrepareTags_AddsManagedBy(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	tags := m.prepareTags(map[string]string{"env": "dev"})

	tagMap := make(map[string]string)
	for _, tag := range tags {
		tagMap[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}

	if tagMap["managed-by"] != "ocfp" {
		t.Errorf("managed-by = %q, want ocfp", tagMap["managed-by"])
	}
	if tagMap["env"] != "dev" {
		t.Errorf("env = %q, want dev", tagMap["env"])
	}
}

func TestPrepareTags_DoesNotOverrideManagedBy(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	tags := m.prepareTags(map[string]string{"managed-by": "custom"})

	tagMap := make(map[string]string)
	for _, tag := range tags {
		tagMap[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}

	if tagMap["managed-by"] != "custom" {
		t.Errorf("managed-by = %q, want custom", tagMap["managed-by"])
	}
}

func TestSetRemote_NeitherSet(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	perm := &ec2types.IpPermission{}
	err := m.setRemote(perm, &cpi.SecurityRule{})
	if err == nil {
		t.Errorf("expected error when neither RemoteIPCIDR nor RemoteGroup is set")
	}
}

// ---- ipRangesMatch ----------------------------------------------------------

func TestIpRangesMatch(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()

	a := []ec2types.IpRange{{CidrIp: aws.String("10.0.0.0/8")}}
	b := []ec2types.IpRange{{CidrIp: aws.String("10.0.0.0/8")}}
	if !m.ipRangesMatch(a, b) {
		t.Errorf("identical ranges: expected match")
	}

	c := []ec2types.IpRange{{CidrIp: aws.String("192.168.0.0/16")}}
	if m.ipRangesMatch(a, c) {
		t.Errorf("different ranges: expected no match")
	}

	if m.ipRangesMatch(a, nil) {
		t.Errorf("different lengths: expected no match")
	}

	if !m.ipRangesMatch(nil, nil) {
		t.Errorf("both nil: expected match (len 0 == 0)")
	}
}

// ---- ipv6RangesMatch --------------------------------------------------------

func TestIpv6RangesMatch(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()

	a := []ec2types.Ipv6Range{{CidrIpv6: aws.String("::/0")}}
	b := []ec2types.Ipv6Range{{CidrIpv6: aws.String("::/0")}}
	if !m.ipv6RangesMatch(a, b) {
		t.Errorf("identical IPv6 ranges: expected match")
	}

	c := []ec2types.Ipv6Range{{CidrIpv6: aws.String("2001:db8::/32")}}
	if m.ipv6RangesMatch(a, c) {
		t.Errorf("different IPv6 ranges: expected no match")
	}

	if m.ipv6RangesMatch(a, nil) {
		t.Errorf("different lengths: expected no match")
	}
}

// ---- userIDGroupPairsMatch --------------------------------------------------

func TestUserIDGroupPairsMatch(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()

	a := []ec2types.UserIdGroupPair{{GroupId: aws.String("sg-abc")}}
	b := []ec2types.UserIdGroupPair{{GroupId: aws.String("sg-abc")}}
	if !m.userIDGroupPairsMatch(a, b) {
		t.Errorf("identical group pairs: expected match")
	}

	c := []ec2types.UserIdGroupPair{{GroupId: aws.String("sg-xyz")}}
	if m.userIDGroupPairsMatch(a, c) {
		t.Errorf("different group pairs: expected no match")
	}

	if m.userIDGroupPairsMatch(a, nil) {
		t.Errorf("different lengths: expected no match")
	}
}

// ---- ipPermissionsMatch -----------------------------------------------------

func TestIpPermissionsMatch_Equal(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	from := int32(80)
	to := int32(80)
	p1 := &ec2types.IpPermission{
		IpProtocol: aws.String("tcp"),
		FromPort:   &from,
		ToPort:     &to,
		IpRanges:   []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
	}
	p2 := &ec2types.IpPermission{
		IpProtocol: aws.String("tcp"),
		FromPort:   &from,
		ToPort:     &to,
		IpRanges:   []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
	}
	if !m.ipPermissionsMatch(p1, p2) {
		t.Errorf("identical permissions: expected match")
	}
}

func TestIpPermissionsMatch_DiffProtocol(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	p1 := &ec2types.IpPermission{IpProtocol: aws.String("tcp")}
	p2 := &ec2types.IpPermission{IpProtocol: aws.String("udp")}
	if m.ipPermissionsMatch(p1, p2) {
		t.Errorf("different protocol: expected no match")
	}
}

// ---- buildCreateInput -------------------------------------------------------

func TestBuildCreateInput_DefaultDescription(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	req := &cpi.CreateSecurityGroupRequest{
		Name:      "test-sg",
		NetworkID: "vpc-abc",
		// Description intentionally empty
	}

	result := m.buildCreateInput(req, nil)

	if aws.ToString(result.GroupName) != "test-sg" {
		t.Errorf("GroupName = %q, want test-sg", aws.ToString(result.GroupName))
	}
	if aws.ToString(result.Description) != "Security group created by OCFP" {
		t.Errorf("Description = %q, want default", aws.ToString(result.Description))
	}
	if aws.ToString(result.VpcId) != "vpc-abc" {
		t.Errorf("VpcId = %q, want vpc-abc", aws.ToString(result.VpcId))
	}
}

func TestBuildCreateInput_CustomDescription(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	req := &cpi.CreateSecurityGroupRequest{
		Name:        "my-sg",
		NetworkID:   "vpc-xyz",
		Description: "custom description",
	}

	result := m.buildCreateInput(req, nil)

	if aws.ToString(result.Description) != "custom description" {
		t.Errorf("Description = %q, want custom description", aws.ToString(result.Description))
	}
}

func TestIpPermissionsMatch_DiffPorts(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	from1, to1 := int32(80), int32(80)
	from2, to2 := int32(443), int32(443)
	p1 := &ec2types.IpPermission{IpProtocol: aws.String("tcp"), FromPort: &from1, ToPort: &to1}
	p2 := &ec2types.IpPermission{IpProtocol: aws.String("tcp"), FromPort: &from2, ToPort: &to2}
	if m.ipPermissionsMatch(p1, p2) {
		t.Errorf("different ports: expected no match")
	}
}

func TestIpPermissionsMatch_DiffIPv6Ranges(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	p1 := &ec2types.IpPermission{
		IpProtocol: aws.String("tcp"),
		Ipv6Ranges: []ec2types.Ipv6Range{{CidrIpv6: aws.String("::/0")}},
	}
	p2 := &ec2types.IpPermission{
		IpProtocol: aws.String("tcp"),
		Ipv6Ranges: []ec2types.Ipv6Range{{CidrIpv6: aws.String("2001:db8::/32")}},
	}
	if m.ipPermissionsMatch(p1, p2) {
		t.Errorf("different IPv6 ranges: expected no match")
	}
}

func TestIpPermissionsMatch_DiffGroupPairs(t *testing.T) {
	t.Parallel()

	m := nilSecurityManager()
	p1 := &ec2types.IpPermission{
		IpProtocol:       aws.String("tcp"),
		UserIdGroupPairs: []ec2types.UserIdGroupPair{{GroupId: aws.String("sg-a")}},
	}
	p2 := &ec2types.IpPermission{
		IpProtocol:       aws.String("tcp"),
		UserIdGroupPairs: []ec2types.UserIdGroupPair{{GroupId: aws.String("sg-b")}},
	}
	if m.ipPermissionsMatch(p1, p2) {
		t.Errorf("different group pairs: expected no match")
	}
}
