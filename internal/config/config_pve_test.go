package config

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minimalPVEConfig returns a Config with PVE provider set and the supplied
// credential fields populated. Other fields are left at zero values so
// blobstore/tailscale/artifacts validators do not fire.
func minimalPVEConfig(authToken, tokenSecret, username, password string) *Config {
	return &Config{
		Name:        "test-pve",
		Provider:    "pve",
		AuthToken:   authToken,
		TokenSecret: tokenSecret,
		Username:    username,
		Password:    password,
	}
}

// TestValidate_PVE_BothEmpty_ReturnsError asserts that validate returns
// ErrPVEAuthRequired when neither API token auth nor user/password auth is
// configured for a PVE bloc.
func TestValidate_PVE_BothEmpty_ReturnsError(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfig("", "", "", "")

	err := validatePVEAuth(cfg, io.Discard)

	require.Error(t, err, "expected error when both auth modes are empty")
	assert.ErrorIs(t, err, ErrPVEAuthRequired)
}

// TestValidate_PVE_PasswordOnly_Valid asserts that validate accepts a PVE
// bloc configured with only username/password auth.
func TestValidate_PVE_PasswordOnly_Valid(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfig("", "", "root@pam", "s3cr3t")

	err := validatePVEAuth(cfg, io.Discard)

	require.NoError(t, err, "expected no error for password-only auth")
}

// TestValidate_PVE_TokenOnly_Valid asserts that validate accepts a PVE bloc
// configured with only API token auth (auth_token + token_secret).
func TestValidate_PVE_TokenOnly_Valid(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfig("root@pam!ocfp-bosh=abc123", "secret-uuid", "", "")

	err := validatePVEAuth(cfg, io.Discard)

	require.NoError(t, err, "expected no error for token-only auth")
}

// TestValidate_PVE_BothSet_WarnsNotErrors asserts that validate does not
// return an error when both auth modes are set, and that a warning is written
// to the provided writer mentioning token precedence.
func TestValidate_PVE_BothSet_WarnsNotErrors(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfig("root@pam!ocfp-bosh=abc123", "secret-uuid", "root@pam", "s3cr3t")

	var buf bytes.Buffer

	validateErr := validatePVEAuth(cfg, &buf)

	// Must not return an error — both modes configured is a warning, not fatal.
	require.NoError(t, validateErr, "expected no error when both auth modes are set")

	// Warning must mention api token precedence.
	output := buf.String()
	assert.True(t, strings.Contains(output, "WARNING"), "expected WARNING in warning output")
	assert.True(t, strings.Contains(output, "api token"), "expected 'api token' mention in warning")
}

// TestValidate_PVE_PartialTokenAuth_ReturnsError asserts that setting only
// auth_token (without token_secret) is treated as incomplete token auth and
// triggers ErrPVEAuthRequired when no password auth is configured either.
func TestValidate_PVE_PartialTokenAuth_ReturnsError(t *testing.T) {
	t.Parallel()

	// auth_token set but token_secret missing — token mode incomplete.
	// username/password also absent — password mode absent.
	cfg := minimalPVEConfig("root@pam!ocfp-bosh=abc123", "", "", "")

	err := validatePVEAuth(cfg, io.Discard)

	require.Error(t, err, "expected error when auth_token is set but token_secret is missing and no password auth configured")
	assert.ErrorIs(t, err, ErrPVEAuthRequired)
}

// TestValidate_PVE_PartialPasswordAuth_ReturnsError asserts that setting only
// password (without username) is treated as incomplete password auth and
// triggers ErrPVEAuthRequired when no token auth is configured either.
func TestValidate_PVE_PartialPasswordAuth_ReturnsError(t *testing.T) {
	t.Parallel()

	// password set but username missing — password mode incomplete.
	// auth_token/token_secret absent — token mode absent.
	cfg := minimalPVEConfig("", "", "", "s3cr3t")

	err := validatePVEAuth(cfg, io.Discard)

	require.Error(t, err, "expected error when password is set but username is missing and no token auth configured")
	assert.ErrorIs(t, err, ErrPVEAuthRequired)
}

// TestValidate_Integration_PVE_BothEmpty asserts that the top-level validate()
// function propagates ErrPVEAuthRequired for a PVE bloc with no credentials.
func TestValidate_Integration_PVE_BothEmpty(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfig("", "", "", "")

	err := validate(cfg)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPVEAuthRequired)
}

// TestValidate_Integration_PVE_TokenOnly asserts that validate() returns nil
// for a PVE bloc with only API token auth set.
func TestValidate_Integration_PVE_TokenOnly(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfig("root@pam!ocfp-bosh=abc123", "secret-uuid", "", "")

	err := validate(cfg)

	require.NoError(t, err)
}

// TestValidate_Integration_PVE_PasswordOnly asserts that validate() returns
// nil for a PVE bloc with only user/password auth set.
func TestValidate_Integration_PVE_PasswordOnly(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfig("", "", "root@pam", "s3cr3t")

	err := validate(cfg)

	require.NoError(t, err)
}

// TestValidate_Integration_NonPVE_NoAuthCheck asserts that validate() does NOT
// return ErrPVEAuthRequired for a non-PVE provider with no credentials set.
// It may return other validation errors, but the PVE auth gate must be silent.
func TestValidate_Integration_NonPVE_NoAuthCheck(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Name:     "test-aws",
		Provider: "aws",
		// No PVE credentials — must not trigger PVE auth check.
	}

	err := validate(cfg)

	if err != nil {
		assert.NotErrorIs(t, err, ErrPVEAuthRequired,
			"non-PVE provider must not trigger ErrPVEAuthRequired; got: %v", err)
	}
}

// --- VMID range validation tests ---

// minimalPVEConfigWithRanges returns a Config with PVE token auth populated
// and the supplied VMID range fields set.
func minimalPVEConfigWithRanges(start, end int) *Config {
	return &Config{
		Name:           "test-pve",
		Provider:       "pve",
		AuthToken:      "root@pam!tok",
		TokenSecret:    "secret",
		VmidRangeStart: start,
		VmidRangeEnd:   end,
	}
}

// TestValidatePVEVMIDRange_BothZero asserts that zero values (unset) are valid;
// defaults apply at CPI config time.
func TestValidatePVEVMIDRange_BothZero(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfigWithRanges(0, 0)
	err := validatePVEVMIDRange(cfg)
	require.NoError(t, err, "both zero must be valid (defaults apply later)")
}

// TestValidatePVEVMIDRange_ValidRange asserts a typical operator range is accepted.
func TestValidatePVEVMIDRange_ValidRange(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfigWithRanges(100, 5999)
	err := validatePVEVMIDRange(cfg)
	require.NoError(t, err, "start=100, end=5999 must be valid")
}

// TestValidatePVEVMIDRange_StartGTEnd_Error (T07) asserts that end <= start
// returns ErrPVEVMIDRangeInvalid.
func TestValidatePVEVMIDRange_StartGTEnd_Error(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfigWithRanges(5000, 100)
	err := validatePVEVMIDRange(cfg)
	require.Error(t, err, "start > end must return an error")
	assert.ErrorIs(t, err, ErrPVEVMIDRangeInvalid)
}

// TestValidatePVEVMIDRange_EqualStartEnd_Error asserts that start == end
// returns ErrPVEVMIDRangeInvalid (end must be strictly greater than start).
func TestValidatePVEVMIDRange_EqualStartEnd_Error(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfigWithRanges(200, 200)
	err := validatePVEVMIDRange(cfg)
	require.Error(t, err, "start == end must return an error")
	assert.ErrorIs(t, err, ErrPVEVMIDRangeInvalid)
}

// TestValidatePVEVMIDRange_NegativeStart_Error (T07b) asserts that a negative
// VmidRangeStart returns ErrPVEVMIDRangeInvalid.
func TestValidatePVEVMIDRange_NegativeStart_Error(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfigWithRanges(-1, 5999)
	err := validatePVEVMIDRange(cfg)
	require.Error(t, err, "negative start must return an error")
	assert.ErrorIs(t, err, ErrPVEVMIDRangeInvalid)
}

// TestValidatePVEVMIDRange_NegativeEnd_Error asserts that a negative VmidRangeEnd
// returns ErrPVEVMIDRangeInvalid.
func TestValidatePVEVMIDRange_NegativeEnd_Error(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfigWithRanges(100, -1)
	err := validatePVEVMIDRange(cfg)
	require.Error(t, err, "negative end must return an error")
	assert.ErrorIs(t, err, ErrPVEVMIDRangeInvalid)
}

// TestValidatePVEVMIDRange_ExceedsMax_Error asserts that a value exceeding
// the PVE maximum VMID (999999999) returns ErrPVEVMIDRangeInvalid.
func TestValidatePVEVMIDRange_ExceedsMax_Error(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfigWithRanges(100, 1_000_000_000)
	err := validatePVEVMIDRange(cfg)
	require.Error(t, err, "end exceeding PVE max (999999999) must return an error")
	assert.ErrorIs(t, err, ErrPVEVMIDRangeInvalid)
}

// TestValidatePVEVMIDRange_MaxBoundary asserts that end == 999999999 is accepted.
func TestValidatePVEVMIDRange_MaxBoundary(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfigWithRanges(100, 999999999)
	err := validatePVEVMIDRange(cfg)
	require.NoError(t, err, "end == 999999999 (PVE max) must be valid")
}

// TestConfig_VmidRangeFields_ZeroValueSafe (T05b) asserts that a Config struct
// at zero value has VmidRangeStart and VmidRangeEnd both zero without panicking.
func TestConfig_VmidRangeFields_ZeroValueSafe(t *testing.T) {
	t.Parallel()

	var cfg Config
	assert.Equal(t, 0, cfg.VmidRangeStart, "zero-value Config must have VmidRangeStart == 0")
	assert.Equal(t, 0, cfg.VmidRangeEnd, "zero-value Config must have VmidRangeEnd == 0")
}

// TestValidate_Integration_PVE_InvalidVMIDRange asserts that validate() returns
// ErrPVEVMIDRangeInvalid when VMID range is misconfigured for a PVE bloc.
func TestValidate_Integration_PVE_InvalidVMIDRange(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfigWithRanges(5000, 100) // start > end
	err := validate(cfg)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPVEVMIDRangeInvalid)
}

// TestValidate_Integration_PVE_ValidVMIDRange asserts that validate() returns
// nil for a PVE bloc with a valid VMID range.
func TestValidate_Integration_PVE_ValidVMIDRange(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfigWithRanges(100, 5999)
	err := validate(cfg)
	require.NoError(t, err)
}

// --- Network pairing validation tests ---

// minimalPVEConfigWithCIDRs returns a PVE Config with token auth set and the
// supplied director/cf-cloud-config CIDRs populated.
func minimalPVEConfigWithCIDRs(directorCIDR, cfCloudConfigCIDR string) *Config {
	return &Config{
		Name:              "test-pve",
		Provider:          "pve",
		AuthToken:         "root@pam!tok",
		TokenSecret:       "secret",
		CFCloudConfigCIDR: cfCloudConfigCIDR,
		Network: NetworkConfig{
			CIDR: directorCIDR,
		},
	}
}

// TestValidate_PVENetworkPairing_MismatchReturnsError asserts that validate()
// returns an error mentioning "tailscale" when Network.CIDR and
// CFCloudConfigCIDR are both set but refer to different networks.
func TestValidate_PVENetworkPairing_MismatchReturnsError(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfigWithCIDRs("192.168.1.0/24", "10.64.64.0/18")

	err := validate(cfg)

	require.Error(t, err, "mismatched CIDRs must return an error")
	assert.True(t, strings.Contains(strings.ToLower(err.Error()), "tailscale"),
		"error must mention tailscale hazard; got: %v", err)
}

// TestValidate_PVENetworkPairing_MatchOK asserts that validate() returns nil
// when Network.CIDR and CFCloudConfigCIDR refer to the same network (host
// bits stripped for comparison).
func TestValidate_PVENetworkPairing_MatchOK(t *testing.T) {
	t.Parallel()

	// Both refer to the same /24 network; host-bit variant intentional.
	cfg := minimalPVEConfigWithCIDRs("192.168.1.0/24", "192.168.1.5/24")

	err := validate(cfg)

	require.NoError(t, err, "matching CIDRs must not return an error")
}

// TestValidate_PVENetworkPairing_OneEmptySkips asserts that validate() returns
// nil when only one CIDR is set (incomplete pair — validator skips).
func TestValidate_PVENetworkPairing_OneEmptySkips(t *testing.T) {
	t.Parallel()

	// Only Network.CIDR set; CFCloudConfigCIDR absent.
	cfg := minimalPVEConfigWithCIDRs("192.168.1.0/24", "")

	err := validate(cfg)

	require.NoError(t, err, "single CIDR set must not trigger network pairing error")
}

// --- Template seed network validation tests ---

// minimalPVEConfigWithTemplateSeed returns a Config with PVE token auth
// populated and the supplied template_seed_* fields set.
func minimalPVEConfigWithTemplateSeed(ip, gateway string, dns []string, searchDomain string) *Config {
	return &Config{
		Name:                     "test-pve",
		Provider:                 "pve",
		AuthToken:                "root@pam!tok",
		TokenSecret:              "secret",
		TemplateSeedIP:           ip,
		TemplateSeedGateway:      gateway,
		TemplateSeedDNS:          dns,
		TemplateSeedSearchDomain: searchDomain,
	}
}

// TestValidateTemplateSeedNet exercises validateTemplateSeedNet's full
// decision table: DHCP mode (all empty), a fully valid static configuration
// shaped after the CHC lab bloc, and every invalid combination it must reject.
func TestValidateTemplateSeedNet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		ip           string
		gateway      string
		dns          []string
		searchDomain string
		wantErr      error
	}{
		{
			name: "all empty is valid DHCP mode",
		},
		{
			name:         "full valid CHC-shaped config",
			ip:           "10.61.148.2/24",
			gateway:      "10.61.148.1",
			dns:          []string{"10.97.160.160", "10.97.160.161"},
			searchDomain: "ldschurch.org",
		},
		{
			name:    "gateway without ip fails",
			gateway: "10.61.148.1",
			wantErr: ErrTemplateSeedIPRequired,
		},
		{
			name:    "dns without ip fails",
			dns:     []string{"10.97.160.160"},
			wantErr: ErrTemplateSeedIPRequired,
		},
		{
			name:         "searchdomain without ip fails",
			searchDomain: "ldschurch.org",
			wantErr:      ErrTemplateSeedIPRequired,
		},
		{
			name:    "bare ip without prefix fails",
			ip:      "10.61.148.2",
			gateway: "10.61.148.1",
			wantErr: ErrTemplateSeedIPInvalid,
		},
		{
			name:    "network address of its own prefix fails",
			ip:      "10.61.148.0/24",
			gateway: "10.61.148.1",
			wantErr: ErrTemplateSeedIPInvalid,
		},
		{
			name:    "broadcast address of its own prefix fails",
			ip:      "10.61.148.255/24",
			gateway: "10.61.148.1",
			wantErr: ErrTemplateSeedIPInvalid,
		},
		{
			name:    "missing gateway fails",
			ip:      "10.61.148.2/24",
			wantErr: ErrTemplateSeedGatewayInvalid,
		},
		{
			name:    "gateway outside prefix fails",
			ip:      "10.61.148.2/26",
			gateway: "10.61.149.1",
			wantErr: ErrTemplateSeedGatewayInvalid,
		},
		{
			name:    "gateway equal to ip fails",
			ip:      "10.61.148.2/24",
			gateway: "10.61.148.2",
			wantErr: ErrTemplateSeedGatewayInvalid,
		},
		{
			name:    "unparseable gateway fails",
			ip:      "10.61.148.2/24",
			gateway: "not-an-ip",
			wantErr: ErrTemplateSeedGatewayInvalid,
		},
		{
			name:    "bad dns entry fails",
			ip:      "10.61.148.2/24",
			gateway: "10.61.148.1",
			dns:     []string{"10.97.160.160", "not-an-ip"},
			wantErr: ErrTemplateSeedDNSInvalid,
		},
		{
			// RFC 3021: a /31 has no network or broadcast address, both
			// addresses are valid hosts, and each is on-link to the other.
			name:    "/31 lower address as ip, upper address as gateway is valid",
			ip:      "10.0.0.0/31",
			gateway: "10.0.0.1",
		},
		{
			name:    "/31 upper address as ip, lower address as gateway is valid",
			ip:      "10.0.0.1/31",
			gateway: "10.0.0.0",
		},
		{
			// A bare /32 has no distinct network or broadcast address either
			// (masking it changes nothing), so it must not be rejected on
			// that basis the way a /24's .0 or .255 would be. It still
			// fails today, on ErrTemplateSeedGatewayInvalid rather than
			// ErrTemplateSeedIPInvalid: gateway containment requires the
			// gateway to be inside the /32, which only the seed address
			// itself satisfies, and the seed address is barred as its own
			// gateway. Full off-link point-to-point support for /32 is a
			// known residual gap (see the function doc comment), not
			// something this test claims is fixed.
			name:    "/32 skips the network/broadcast rejection, still fails on gateway containment",
			ip:      "10.61.148.2/32",
			gateway: "10.61.148.1",
			wantErr: ErrTemplateSeedGatewayInvalid,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg := minimalPVEConfigWithTemplateSeed(tc.ip, tc.gateway, tc.dns, tc.searchDomain)
			err := validateTemplateSeedNet(cfg)

			if tc.wantErr == nil {
				require.NoError(t, err, "expected no error for %s", tc.name)

				return
			}

			require.Error(t, err, "expected error for %s", tc.name)
			assert.ErrorIs(t, err, tc.wantErr)
		})
	}
}

// TestValidate_Integration_PVE_TemplateSeedNetWired asserts that validate()
// propagates ErrTemplateSeedIPRequired for a PVE bloc with the gateway set
// but no template_seed_ip, confirming validateTemplateSeedNet is wired into
// the validatePVE chain.
func TestValidate_Integration_PVE_TemplateSeedNetWired(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfigWithTemplateSeed("", "10.61.148.1", nil, "")

	err := validate(cfg)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTemplateSeedIPRequired)
}

// TestValidate_Integration_PVE_TemplateSeedNetValid asserts that validate()
// returns nil for a PVE bloc with a fully valid static template seed
// configuration.
func TestValidate_Integration_PVE_TemplateSeedNetValid(t *testing.T) {
	t.Parallel()

	cfg := minimalPVEConfigWithTemplateSeed(
		"10.61.148.2/24", "10.61.148.1",
		[]string{"10.97.160.160", "10.97.160.161"}, "ldschurch.org",
	)

	err := validate(cfg)

	require.NoError(t, err)
}

// Note: an earlier TestConfig_TemplateSeedFields_ZeroValueSafe asserted
// only that `var cfg Config` has the four template_seed_* fields at their
// Go zero value, which cannot fail for any implementation (Go guarantees
// zero values) and was removed as tautological (m6 in the static-seed
// adversarial review). Its intended purpose — pinning the DHCP-by-default
// backward-compatibility contract — is what
// TestSeedBastionTemplate_MergedPUT_DHCPMode in templates_test.go actually
// verifies, end to end, against the real merged PUT body.

// TestConfig_TemplateSeedFields_YAMLRoundTrip verifies the four
// template_seed_* snake_case YAML keys bind to the correct Config fields
// through a full bloc load, guarding the tag spelling — especially
// template_seed_searchdomain, easy to typo against the "search_domain" or
// "searchDomain" forms a reader might expect.
func TestConfig_TemplateSeedFields_YAMLRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yml")

	yml := []byte("" +
		"blocs:\n" +
		"  chc-lab:\n" +
		"    provider: pve\n" +
		"    api_endpoint: https://pve.example.com:8006\n" +
		"    auth_token: root@pam!tok\n" +
		"    token_secret: secret\n" +
		"    region: pve01\n" +
		"    template_bridge: vlan54\n" +
		"    template_seed_ip: 10.61.148.2/24\n" +
		"    template_seed_gateway: 10.61.148.1\n" +
		"    template_seed_dns:\n" +
		"      - 10.97.160.160\n" +
		"      - 10.97.160.161\n" +
		"    template_seed_searchdomain: ldschurch.org\n")

	if err := os.WriteFile(cfgPath, yml, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadWithParams(cfgPath, "chc-lab")
	if err != nil {
		t.Fatalf("LoadWithParams: %v", err)
	}

	assert.Equal(t, "10.61.148.2/24", cfg.TemplateSeedIP)
	assert.Equal(t, "10.61.148.1", cfg.TemplateSeedGateway)
	assert.Equal(t, []string{"10.97.160.160", "10.97.160.161"}, cfg.TemplateSeedDNS)
	assert.Equal(t, "ldschurch.org", cfg.TemplateSeedSearchDomain)
}
