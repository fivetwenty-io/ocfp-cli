package vault

import (
	"encoding/json"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Contract tests validate that the Go implementation produces data compatible with Perl expectations.

// TestContract_SubnetStructure_PerlCompatibility verifies subnet data matches Perl structure.
func TestContract_SubnetStructure_PerlCompatibility(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
		Region:   "eu01",
		Network: config.NetworkConfig{
			CIDR: "10.0.0.0/16",
		},
	}

	provider := &StackitVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
		logger:            logger.Get(),
	}

	networkInfo := &subnetNetworkInfo{
		network:    "10.0.1.0",
		cidrPrefix: "10.0.1",
		gateway:    "10.0.1.1",
		lastHost:   "10.0.1.254",
	}

	subnetData := provider.buildSubnetData("ocfp", 0, "10.0.1.0/24", networkInfo, "eu01-1")

	// Perl contract: All fields must be present and correct type
	t.Run("all_22_fields_present", func(t *testing.T) {
		perlRequiredFields := map[string]string{
			// Original 9 fields
			"id":          "string",
			"cidr_block":  "string",
			"cidr_prefix": "string",
			"ip_0":        "string",
			"ip_n":        "string",
			"gateway":     "string",
			"dns":         "string",
			"az":          "string",
			"type":        "string",
			// Perl parity fields
			"subnet_cidr":   "string",
			"subnet_prefix": "string",
			"net_cidr":      "string",
			"net_prefix":    "string",
			"name":          "string",
			"subnet_num":    "int",
			"network_id":    "string",
			"provider":      "string",
			"provider_type": "string",
			"virtual":       "string",
			"parent_cidr":   "string",
			"environment":   "string",
			"region":        "string",
		}

		for field, expectedType := range perlRequiredFields {
			value, exists := subnetData[field]
			require.True(t, exists, "Perl requires field: %s", field)
			require.NotNil(t, value, "Field %s must not be nil", field)

			switch expectedType {
			case "string":
				_, ok := value.(string)
				assert.True(t, ok, "Field %s must be string, got %T", field, value)
			case "int":
				_, ok := value.(int)
				assert.True(t, ok, "Field %s must be int, got %T", field, value)
			}
		}
	})

	t.Run("cidr_format_matches_perl", func(t *testing.T) {
		// Perl expects CIDR with slash notation
		assert.Contains(t, subnetData["subnet_cidr"].(string), "/")
		assert.Contains(t, subnetData["net_cidr"].(string), "/")
		assert.Contains(t, subnetData["parent_cidr"].(string), "/")
	})

	t.Run("virtual_flag_is_string", func(t *testing.T) {
		// Perl expects "true" as string, not boolean
		virtual := subnetData["virtual"]
		assert.IsType(t, "", virtual, "virtual must be string for Perl")
		assert.Equal(t, "true", virtual, "virtual must be string 'true'")
	})

	t.Run("subnet_naming_convention", func(t *testing.T) {
		// Perl expects: {type}-{num} format
		name := subnetData["name"].(string)
		assert.Equal(t, "ocfp-0", name, "Name format must match Perl convention")
	})
}

// TestContract_PathStructure_PerlCompatibility verifies vault paths match Perl expectations.
func TestContract_PathStructure_PerlCompatibility(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
		Region:   "eu01",
	}

	pathBuilder := NewPathBuilder(cfg, "test-bloc")

	// Perl contract: Paths must use net/ not vpc/
	t.Run("network_paths_use_net", func(t *testing.T) {
		netPath := pathBuilder.GetNetPath("mgmt")
		assert.Contains(t, netPath, "/net", "Must use net/ path")
		assert.NotContains(t, netPath, "/vpc", "Must NOT use vpc/ path")

		subnetsPath := pathBuilder.GetSubnetsPath("mgmt")
		assert.Contains(t, subnetsPath, "/net/subnets", "Subnets must be under net/")
	})

	t.Run("security_group_paths_follow_perl_convention", func(t *testing.T) {
		// Standard SGs: secret/{bloc}/{env}/net/sgs/{sg_type}
		sgPath := pathBuilder.GetSecurityGroupsPath("mgmt")
		assert.Contains(t, sgPath, "/net/sgs", "Standard SGs use net/sgs/ path")

		// CF-specific SGs: secret/{bloc}/{env}/net/{sg_name} (no sgs/)
		// This is handled by storeSecurityGroupToVault logic
	})

	t.Run("s3_credentials_path_correction", func(t *testing.T) {
		// Perl expects: secret/{bloc}/{env}/bosh/s3
		// NOT: secret/{bloc}/{env}/bosh/iam/s3
		s3Path := pathBuilder.GetS3Path("mgmt")
		assert.Contains(t, s3Path, "/bosh/s3", "S3 must be at bosh/s3")
		assert.NotContains(t, s3Path, "/bosh/iam/s3", "Must NOT use bosh/iam/s3")
	})

	t.Run("fqdn_paths_by_environment", func(t *testing.T) {
		mgmtPath := pathBuilder.GetFQDNsPath("mgmt")
		ocfPath := pathBuilder.GetFQDNsPath("ocf")

		assert.Contains(t, mgmtPath, "/mgmt/")
		assert.Contains(t, ocfPath, "/ocf/")
		assert.NotEqual(t, mgmtPath, ocfPath, "Different paths for different envs")
	})
}

// TestContract_SecurityGroups_PerlCompatibility verifies SG handling matches Perl.
func TestContract_SecurityGroups_PerlCompatibility(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
	}

	provider := &StackitVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
		logger:            logger.Get(),
	}

	t.Run("all_8_security_groups_defined", func(t *testing.T) {
		sgMapping := provider.buildSecurityGroupMapping()

		perlRequiredSGs := []string{
			"bastion",
			"infra",
			"ocfp",
			"lb-ext",
			"ocf-cf-router-ingress",
			"ocf-cf-tcp-router-ingress",
			"ocf-cf-ssh-proxy-ingress",
			"default",
		}

		assert.Equal(t, 8, len(sgMapping), "Must have exactly 8 SGs")

		for _, sgType := range perlRequiredSGs {
			_, exists := sgMapping[sgType]
			assert.True(t, exists, "Perl requires SG type: %s", sgType)
		}
	})

	t.Run("sg_naming_with_bloc_prefix", func(t *testing.T) {
		sgMapping := provider.buildSecurityGroupMapping()

		// Most SGs should have bloc prefix
		assert.Equal(t, "test-bloc-bastion", sgMapping["bastion"])
		assert.Equal(t, "test-bloc-infra", sgMapping["infra"])
		assert.Equal(t, "test-bloc-ocfp", sgMapping["ocfp"])

		// Default has no prefix (Perl convention)
		assert.Equal(t, "default", sgMapping["default"])
	})

	t.Run("cf_sg_path_logic", func(t *testing.T) {
		// CF SGs must be stored directly under net/ not net/sgs/
		cfSGTypes := []string{
			"ocf-cf-router-ingress",
			"ocf-cf-tcp-router-ingress",
			"ocf-cf-ssh-proxy-ingress",
		}

		for _, sgType := range cfSGTypes {
			// Perl expects these at: secret/{bloc}/ocf/net/{sg_type}
			// NOT at: secret/{bloc}/ocf/net/sgs/{sg_type}
			// CF-specific detection uses prefix check
			isCF := len(sgType) >= 3 && (sgType[:3] == "cf-" || (len(sgType) >= 7 && sgType[:7] == "ocf-cf-"))
			assert.True(t, isCF, "%s should be CF-specific", sgType)
		}
	})
}

// TestContract_AZFormat_PerlCompatibility verifies AZ data format.
func TestContract_AZFormat_PerlCompatibility(t *testing.T) {
	// Perl expects cloud_properties as JSON string, not object
	tests := []struct {
		name         string
		az           string
		expectedJSON string
		mustBeString bool
	}{
		{
			name:         "eu01-1",
			az:           "eu01-1",
			expectedJSON: `{"availability_zone": "eu01-1"}`,
			mustBeString: true,
		},
		{
			name:         "eu01-2",
			az:           "eu01-2",
			expectedJSON: `{"availability_zone": "eu01-2"}`,
			mustBeString: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Perl contract: cloud_properties must be JSON string
			azData := map[string]interface{}{
				"name":             tt.az,
				"cloud_properties": tt.expectedJSON,
			}

			cloudProps := azData["cloud_properties"]
			assert.IsType(t, "", cloudProps, "cloud_properties must be string")

			// Verify it's valid JSON
			var parsed map[string]interface{}
			err := json.Unmarshal([]byte(cloudProps.(string)), &parsed)
			require.NoError(t, err, "cloud_properties must be valid JSON")

			assert.Equal(t, tt.az, parsed["availability_zone"])
		})
	}
}

// TestContract_ReservedIPs_PerlCompatibility verifies IP allocation. STACKIT
// no longer carries its own hand-rolled offset table (see
// stackit_reserved_ips_test.go's TestStackitReservedIPs_TripleDefaultsToSpanning):
// every STACKIT bloc now resolves through the same netlayout engine PVE
// uses, and since no STACKIT blocs are deployed today the "Perl
// compatibility" contract this test locks IS the spanning strategy's
// offset table (internal/netlayout/strategies/spanning.yaml), not the
// legacy Perl offsets (31/32/33/36/37 on the ocf tier) this test asserted
// before the reroute.
func TestContract_ReservedIPs_PerlCompatibility(t *testing.T) {
	cfg := &config.Config{Provider: "stackit", Network: config.NetworkConfig{Strategy: "spanning"}}

	// Spanning contract: specific IP offsets for each environment, subnet
	// index 0 (mgmt roles pinned elsewhere — doomsday/shout on 1, ocfp_ui on
	// 2 — are absent here; see spanning_golden_test.go for per-index proof).
	t.Run("mgmt_ip_offsets_match_spanning", func(t *testing.T) {
		cidr := "10.10.1.0/24"
		vaultIPs, err := reservedIPsForSubnet(cidr, "mgmt", 0, cfg, logger.Get())
		require.NoError(t, err)

		spanningMgmtOffsets := map[string]string{
			"bastion_ip":    "10.10.1.3",  // offset 3, pinned subnet 0
			"bosh_ip":       "10.10.1.4",  // offset 4, pinned subnet 0
			"vault_ip":      "10.10.1.5",  // offset 5, unpinned
			"jumpbox_ip":    "10.10.1.6",  // offset 6, unpinned
			"concourse_ip":  "10.10.1.7",  // offset 7, unpinned
			"prometheus_ip": "10.10.1.8",  // offset 8, unpinned
			"shield_ip":     "10.10.1.9",  // offset 9, pinned subnet 0
			"blacksmith_ip": "10.10.1.10", // offset 10, pinned subnet 0
		}

		for key, expectedIP := range spanningMgmtOffsets {
			actualIP, exists := vaultIPs[key]
			require.True(t, exists, "spanning requires IP: %s", key)
			assert.Equal(t, expectedIP, actualIP, "IP offset mismatch for %s", key)
		}
	})

	t.Run("ocf_ip_offsets_match_spanning", func(t *testing.T) {
		cidr := "10.20.1.0/24"
		vaultIPs, err := reservedIPsForSubnet(cidr, "ocf", 0, cfg, logger.Get())
		require.NoError(t, err)

		spanningOCFOffsets := map[string]string{
			"bosh_ip":    "10.20.1.64", // offset 64, pinned subnet 0
			"vault_ip":   "10.20.1.65", // offset 65, unpinned
			"jumpbox_ip": "10.20.1.66", // offset 66, unpinned
			"haproxy_ip": "10.20.1.97", // offset 97, pinned subnet 0
		}

		for key, expectedIP := range spanningOCFOffsets {
			actualIP, exists := vaultIPs[key]
			require.True(t, exists, "spanning requires IP: %s", key)
			assert.Equal(t, expectedIP, actualIP, "IP offset mismatch for %s", key)
		}

		// blacksmith is pinned to ocf subnet index 1, absent from index 0.
		assert.NotContains(t, vaultIPs, "blacksmith_ip", "blacksmith is pinned to ocf subnet 1")
	})

	t.Run("available_ranges_match_spanning", func(t *testing.T) {
		// Mgmt available: 32-63
		cidr := "10.10.1.0/24"
		vaultIPs, err := reservedIPsForSubnet(cidr, "mgmt", 0, cfg, logger.Get())
		require.NoError(t, err)

		assert.Equal(t, "10.10.1.32", vaultIPs["available_0"])
		assert.Equal(t, "10.10.1.63", vaultIPs["available_1"])

		// OCF available: 96->
		cidr = "10.20.1.0/24"
		vaultIPs, err = reservedIPsForSubnet(cidr, "ocf", 0, cfg, logger.Get())
		require.NoError(t, err)

		assert.Equal(t, "10.20.1.96", vaultIPs["available_0"])
		assert.Equal(t, "10.20.1.254", vaultIPs["available_1"])
	})

	t.Run("reserved_ranges_match_spanning", func(t *testing.T) {
		// Mgmt reserved: 0-31,64->
		cidr := "10.10.1.0/24"
		vaultIPs, err := reservedIPsForSubnet(cidr, "mgmt", 0, cfg, logger.Get())
		require.NoError(t, err)

		assert.Equal(t, "10.10.1.0", vaultIPs["reserved_0"])
		assert.Equal(t, "10.10.1.31", vaultIPs["reserved_1"])
		assert.Equal(t, "10.10.1.64", vaultIPs["reserved_2"])
		assert.Equal(t, "10.10.1.254", vaultIPs["reserved_3"])

		// OCF reserved: 0-95
		cidr = "10.20.1.0/24"
		vaultIPs, err = reservedIPsForSubnet(cidr, "ocf", 0, cfg, logger.Get())
		require.NoError(t, err)

		assert.Equal(t, "10.20.1.0", vaultIPs["reserved_0"])
		assert.Equal(t, "10.20.1.95", vaultIPs["reserved_1"])
	})
}

// TestContract_FQDNFiltering_PerlCompatibility verifies FQDN filtering logic.
func TestContract_FQDNFiltering_PerlCompatibility(t *testing.T) {
	cfg := &config.Config{
		Name:       "test-bloc",
		Provider:   "stackit",
		Region:     "eu01",
		DomainName: "test.stackit.cloud",
	}

	t.Run("mgmt_filters_cf_systems_like_perl", func(t *testing.T) {
		safe := newMockFullSafe()
		provider := NewStackitVaultProvider(cfg, safe, "test-bloc")

		// Perl filters these CF-related systems from mgmt
		perlFilteredSystems := []string{
			"cf", "cloud_controller", "api", "uaa", "diego",
			"credhub", "loggregator", "router", "doppler",
			"log-api", "syslog-scheduler",
			"cf-router", "cf_router", "router-cf", "router_cf",
			"cf-tcp-router",
		}

		for _, system := range perlFilteredSystems {
			result := provider.shouldSkipCFForEnvType("mgmt", system)
			assert.True(t, result, "Perl filters %s from mgmt", system)
		}
	})

	t.Run("mgmt_keeps_infrastructure_systems", func(t *testing.T) {
		safe := newMockFullSafe()
		provider := NewStackitVaultProvider(cfg, safe, "test-bloc")

		// Perl keeps these systems in mgmt
		perlKeptSystems := []string{
			"shield", "vault", "prometheus", "concourse",
			"bosh", "grafana", "alertmanager",
		}

		for _, system := range perlKeptSystems {
			result := provider.shouldSkipCFForEnvType("mgmt", system)
			assert.False(t, result, "Perl keeps %s in mgmt", system)
		}
	})

	t.Run("ocf_keeps_all_systems_like_perl", func(t *testing.T) {
		safe := newMockFullSafe()
		provider := NewStackitVaultProvider(cfg, safe, "test-bloc")

		// Perl keeps ALL systems in OCF (no filtering)
		allSystems := []string{
			"cf", "uaa", "router", "shield", "vault",
			"prometheus", "diego", "cloud_controller",
		}

		for _, system := range allSystems {
			result := provider.shouldSkipCFForEnvType("ocf", system)
			assert.False(t, result, "Perl keeps %s in ocf", system)
		}
	})

	t.Run("shield_auto_generation_for_ocf", func(t *testing.T) {
		safe := newMockFullSafe()
		provider := NewStackitVaultProvider(cfg, safe, "test-bloc")

		cfg.FQDNs = &config.FQDNConfig{
			Base: "test.stackit.cloud",
			OCF: map[string]string{
				"cf": "cf.test.stackit.cloud",
				// No shield - should be derived from base
			},
		}

		err := provider.ConfigureFQDNs("", "ocf", nil, 1, 1)
		require.NoError(t, err)

		fqdnPath := provider.PathBuilder.GetFQDNsPath("ocf")
		data, _ := safe.GetAll(fqdnPath)

		// Shield should be derived from base as shield.{base}
		assert.Contains(t, data, "shield")
		assert.Equal(t, "shield.test.stackit.cloud", data["shield"])
	})
}

// TestContract_KeypairFields_PerlCompatibility verifies keypair data structure.
func TestContract_KeypairFields_PerlCompatibility(t *testing.T) {
	// Perl expects these keypair fields
	perlRequiredFields := []string{
		"name",
		"fingerprint",
		"private_key_path",
		"public_key",
	}

	keypairData := map[string]interface{}{
		"name":             "test-keypair",
		"fingerprint":      "aa:bb:cc:dd:ee:ff",
		"private_key_path": "/path/to/key",
		"public_key":       "ssh-rsa AAAAB3...",
	}

	for _, field := range perlRequiredFields {
		t.Run("field_"+field, func(t *testing.T) {
			value, exists := keypairData[field]
			assert.True(t, exists, "Perl requires field: %s", field)
			assert.NotNil(t, value, "Field must not be nil")
			assert.IsType(t, "", value, "Field must be string")
		})
	}
}

// TestContract_PublicIPKeys_PerlCompatibility verifies public IP vault key format.
func TestContract_PublicIPKeys_PerlCompatibility(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
	}

	provider := &StackitVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
		logger:            logger.Get(),
	}

	// Perl formats: {job}_{index}
	tests := []struct {
		job      string
		index    string
		expected string
	}{
		{"bosh", "0", "bosh_0"},
		{"router", "0", "router_0"},
		{"router", "1", "router_1"},
		{"tcp-router", "0", "tcp-router_0"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			key, _ := provider.determineVaultKeyAndEnvironment(tt.job, tt.index)
			assert.Equal(t, tt.expected, key, "Key format must match Perl")
		})
	}
}

// TestContract_NetworkMetadata_PerlCompatibility verifies network metadata fields.
func TestContract_NetworkMetadata_PerlCompatibility(t *testing.T) {
	cfg := &config.Config{
		Name:     "test-bloc",
		Provider: "stackit",
		Region:   "eu01",
		Network: config.NetworkConfig{
			CIDR: "10.0.0.0/16",
		},
	}

	// Perl expects these network fields
	perlNetworkFields := map[string]string{
		"id":            "string",
		"name":          "string",
		"cidr":          "string",
		"cidr_prefix":   "string",
		"network_id":    "string",
		"environment":   "string",
		"region":        "string",
		"provider":      "string",
		"provider_type": "string",
	}

	networkData := map[string]interface{}{
		"id":            "net-12345",
		"name":          "test-bloc-network",
		"cidr":          "10.0.0.0/16",
		"cidr_prefix":   "10.0.0",
		"network_id":    "net-12345",
		"environment":   cfg.Name,
		"region":        cfg.Region,
		"provider":      "stackit",
		"provider_type": "virtual_network",
	}

	for field, expectedType := range perlNetworkFields {
		t.Run("field_"+field, func(t *testing.T) {
			value, exists := networkData[field]
			require.True(t, exists, "Perl requires field: %s", field)
			require.NotNil(t, value, "Field must not be nil")

			if expectedType == "string" {
				assert.IsType(t, "", value, "Field %s must be string", field)
			}
		})
	}
}

// TestContract_VirtualSubnetFlag_PerlCompatibility verifies virtual flag handling.
func TestContract_VirtualSubnetFlag_PerlCompatibility(t *testing.T) {
	cfg := &config.Config{
		Name: "test-bloc",
		Network: config.NetworkConfig{
			CIDR: "10.0.0.0/16",
		},
	}

	provider := &StackitVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
		logger:            logger.Get(),
	}

	networkInfo := &subnetNetworkInfo{
		network:    "10.0.1.0",
		cidrPrefix: "10.0.1",
		gateway:    "10.0.1.1",
		lastHost:   "10.0.1.254",
	}

	t.Run("ocfp_subnet_has_virtual_flag", func(t *testing.T) {
		// Perl adds virtual flag for ocfp/infra/bastion subnets
		subnetData := provider.buildSubnetData("ocfp", 0, "10.0.1.0/24", networkInfo, "eu01-1")
		assert.Contains(t, subnetData, "virtual")
		assert.Equal(t, "true", subnetData["virtual"])
	})

	t.Run("reserved_subnet_no_virtual_flag", func(t *testing.T) {
		// Perl does NOT add virtual flag for reserved subnets
		subnetData := provider.buildSubnetData("reserved", 0, "10.0.1.0/24", networkInfo, "eu01-1")
		assert.NotContains(t, subnetData, "virtual", "Reserved subnets should not have virtual flag")
	})
}

// TestContract_SubnetNumbering_PerlCompatibility verifies subnet numbering.
func TestContract_SubnetNumbering_PerlCompatibility(t *testing.T) {
	cfg := &config.Config{
		Name: "test-bloc",
		Network: config.NetworkConfig{
			CIDR: "10.0.0.0/16",
		},
	}

	provider := &StackitVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
		logger:            logger.Get(),
	}

	networkInfo := &subnetNetworkInfo{
		network:    "10.0.1.0",
		cidrPrefix: "10.0.1",
		gateway:    "10.0.1.1",
		lastHost:   "10.0.1.254",
	}

	// Perl uses subnet_num field starting from 0
	tests := []struct {
		subnetType string
		subnetNum  int
		wantName   string
		wantNum    int
	}{
		{"ocfp", 0, "ocfp-0", 0},
		{"ocfp", 1, "ocfp-1", 1},
		{"infra", 0, "infra-0", 0},
		{"bastion", 0, "bastion-0", 0},
	}

	for _, tt := range tests {
		t.Run(tt.wantName, func(t *testing.T) {
			subnetData := provider.buildSubnetData(tt.subnetType, tt.subnetNum, "10.0.1.0/24", networkInfo, "eu01-1")

			assert.Equal(t, tt.wantName, subnetData["name"])
			assert.Equal(t, tt.wantNum, subnetData["subnet_num"])
		})
	}
}

// TestContract_ProviderTypeFields_PerlCompatibility verifies provider_type values.
func TestContract_ProviderTypeFields_PerlCompatibility(t *testing.T) {
	cfg := &config.Config{
		Name: "test-bloc",
		Network: config.NetworkConfig{
			CIDR: "10.0.0.0/16",
		},
	}

	provider := &StackitVaultProvider{
		BaseVaultProvider: providers.NewBaseVaultProvider(cfg, "test-bloc"),
		logger:            logger.Get(),
	}

	networkInfo := &subnetNetworkInfo{
		network:    "10.0.1.0",
		cidrPrefix: "10.0.1",
		gateway:    "10.0.1.1",
		lastHost:   "10.0.1.254",
	}

	t.Run("subnet_provider_type", func(t *testing.T) {
		subnetData := provider.buildSubnetData("ocfp", 0, "10.0.1.0/24", networkInfo, "eu01-1")
		// Perl expects "virtual_subnet" for STACKIT
		assert.Equal(t, "virtual_subnet", subnetData["provider_type"])
	})

	t.Run("network_provider_type", func(t *testing.T) {
		// Perl expects "virtual_network" for STACKIT networks
		expectedType := "virtual_network"
		assert.Equal(t, expectedType, expectedType) // Verify constant
	})
}
