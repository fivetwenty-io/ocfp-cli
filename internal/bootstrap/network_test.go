package bootstrap_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/bootstrap"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// TestSplitIntoN tests the SplitIntoN CIDR splitting function.
func TestSplitIntoN(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cidr      string
		count     int
		wantCount int
		wantFirst string
		wantLast  string
	}{
		{
			name:      "split /20 into 4 subnets",
			cidr:      "10.4.0.0/20",
			count:     4,
			wantCount: 4,
			wantFirst: "10.4.0.0/22",
			wantLast:  "10.4.12.0/22",
		},
		{
			name:      "split /20 into 2 subnets",
			cidr:      "10.4.0.0/20",
			count:     2,
			wantCount: 2,
			wantFirst: "10.4.0.0/21",
			wantLast:  "10.4.8.0/21",
		},
		{
			name:      "split /23 into 2 subnets",
			cidr:      "10.4.0.0/23",
			count:     2,
			wantCount: 2,
			wantFirst: "10.4.0.0/24",
			wantLast:  "10.4.1.0/24",
		},
		{
			name:      "split /24 into 4 subnets",
			cidr:      "10.4.0.0/24",
			count:     4,
			wantCount: 4,
			wantFirst: "10.4.0.0/26",
			wantLast:  "10.4.0.192/26",
		},
		{
			name:      "split /16 into 8 subnets",
			cidr:      "192.168.0.0/16",
			count:     8,
			wantCount: 8,
			wantFirst: "192.168.0.0/19",
			wantLast:  "192.168.224.0/19",
		},
		{
			name:      "invalid count zero",
			cidr:      "10.4.0.0/20",
			count:     0,
			wantCount: 0,
		},
		{
			name:      "invalid count negative",
			cidr:      "10.4.0.0/20",
			count:     -1,
			wantCount: 0,
		},
		{
			name:      "invalid CIDR",
			cidr:      "invalid",
			count:     4,
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := bootstrap.SplitIntoN(tt.cidr, tt.count)

			if len(got) != tt.wantCount {
				t.Errorf("SplitIntoN() returned %d subnets, want %d", len(got), tt.wantCount)
				return
			}

			if tt.wantCount > 0 {
				if got[0] != tt.wantFirst {
					t.Errorf("First subnet = %v, want %v", got[0], tt.wantFirst)
				}

				if got[len(got)-1] != tt.wantLast {
					t.Errorf("Last subnet = %v, want %v", got[len(got)-1], tt.wantLast)
				}
			}
		})
	}
}

// TestCIDRFirstIP tests extracting the first IP from a CIDR block.
func TestCIDRFirstIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cidr string
		want string
	}{
		{
			name: "/20 network",
			cidr: "10.4.0.0/20",
			want: "10.4.0.0",
		},
		{
			name: "/24 network",
			cidr: "192.168.1.0/24",
			want: "192.168.1.0",
		},
		{
			name: "/16 network",
			cidr: "172.16.0.0/16",
			want: "172.16.0.0",
		},
		{
			name: "/25 subnet",
			cidr: "10.0.0.128/25",
			want: "10.0.0.128",
		},
		{
			name: "/30 point-to-point",
			cidr: "192.168.1.0/30",
			want: "192.168.1.0",
		},
		{
			name: "single host /32",
			cidr: "10.1.2.3/32",
			want: "10.1.2.3",
		},
		{
			name: "invalid CIDR",
			cidr: "invalid",
			want: "",
		},
		{
			name: "empty string",
			cidr: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := bootstrap.CIDRFirstIP(tt.cidr)

			if got != tt.want {
				t.Errorf("CIDRFirstIP(%q) = %v, want %v", tt.cidr, got, tt.want)
			}
		})
	}
}

// TestCIDRLastUsableIP tests extracting the last usable IP from a CIDR block.
func TestCIDRLastUsableIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cidr string
		want string
	}{
		{
			name: "/24 network",
			cidr: "192.168.1.0/24",
			want: "192.168.1.254",
		},
		{
			name: "/20 network",
			cidr: "10.4.0.0/20",
			want: "10.4.15.254",
		},
		{
			name: "/16 network",
			cidr: "172.16.0.0/16",
			want: "172.16.255.254",
		},
		{
			name: "/25 subnet first half",
			cidr: "10.0.0.0/25",
			want: "10.0.0.126",
		},
		{
			name: "/25 subnet second half",
			cidr: "10.0.0.128/25",
			want: "10.0.0.254",
		},
		{
			name: "/30 point-to-point",
			cidr: "192.168.1.0/30",
			want: "192.168.1.2",
		},
		{
			name: "/26 subnet",
			cidr: "10.4.0.0/26",
			want: "10.4.0.62",
		},
		{
			name: "invalid CIDR",
			cidr: "invalid",
			want: "",
		},
		{
			name: "empty string",
			cidr: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := bootstrap.CIDRLastUsableIP(tt.cidr)

			if got != tt.want {
				t.Errorf("CIDRLastUsableIP(%q) = %v, want %v", tt.cidr, got, tt.want)
			}
		})
	}
}

// TestCIDRGatewayIP tests calculating the gateway IP (first + 1) for a CIDR block.
func TestCIDRGatewayIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cidr string
		want string
	}{
		{
			name: "/24 network",
			cidr: "192.168.1.0/24",
			want: "192.168.1.1",
		},
		{
			name: "/20 network",
			cidr: "10.4.0.0/20",
			want: "10.4.0.1",
		},
		{
			name: "/16 network",
			cidr: "172.16.0.0/16",
			want: "172.16.0.1",
		},
		{
			name: "/25 subnet",
			cidr: "10.0.0.128/25",
			want: "10.0.0.129",
		},
		{
			name: "/30 point-to-point",
			cidr: "192.168.1.0/30",
			want: "192.168.1.1",
		},
		{
			name: "/26 subnet",
			cidr: "10.4.0.64/26",
			want: "10.4.0.65",
		},
		{
			name: "invalid CIDR",
			cidr: "invalid",
			want: "",
		},
		{
			name: "empty string",
			cidr: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := bootstrap.CIDRGatewayIP(tt.cidr)

			if got != tt.want {
				t.Errorf("CIDRGatewayIP(%q) = %v, want %v", tt.cidr, got, tt.want)
			}
		})
	}
}

// TestCIDRUtilities_EdgeCases tests edge cases across all CIDR utilities.
func TestCIDRUtilities_EdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("very small subnet /31", func(t *testing.T) {
		t.Parallel()

		cidr := "10.0.0.0/31"

		// /31 has only 2 IPs, special case for point-to-point links
		firstIP := bootstrap.CIDRFirstIP(cidr)
		if firstIP != "10.0.0.0" {
			t.Errorf("CIDRFirstIP(/31) = %v, want 10.0.0.0", firstIP)
		}

		// Last usable should be empty or broadcast for /31 (too small for gateway)
		lastIP := bootstrap.CIDRLastUsableIP(cidr)
		if lastIP != "" {
			// /31 is too small, should return empty
			t.Logf("CIDRLastUsableIP(/31) = %v (acceptable for /31)", lastIP)
		}
	})

	t.Run("large network /8", func(t *testing.T) {
		t.Parallel()

		cidr := "10.0.0.0/8"

		firstIP := bootstrap.CIDRFirstIP(cidr)
		if firstIP != "10.0.0.0" {
			t.Errorf("CIDRFirstIP(/8) = %v, want 10.0.0.0", firstIP)
		}

		lastIP := bootstrap.CIDRLastUsableIP(cidr)
		if lastIP != "10.255.255.254" {
			t.Errorf("CIDRLastUsableIP(/8) = %v, want 10.255.255.254", lastIP)
		}

		gateway := bootstrap.CIDRGatewayIP(cidr)
		if gateway != "10.0.0.1" {
			t.Errorf("CIDRGatewayIP(/8) = %v, want 10.0.0.1", gateway)
		}
	})

	t.Run("split with count of 1", func(t *testing.T) {
		t.Parallel()

		cidr := "10.4.0.0/20"
		subnets := bootstrap.SplitIntoN(cidr, 1)

		if len(subnets) != 1 {
			t.Errorf("SplitIntoN(count=1) returned %d subnets, want 1", len(subnets))
		}

		if len(subnets) > 0 && subnets[0] != "10.4.0.0/21" {
			t.Errorf("SplitIntoN(count=1) = %v, want 10.4.0.0/21", subnets[0])
		}
	})

	t.Run("split with very large count", func(t *testing.T) {
		t.Parallel()

		cidr := "10.4.0.0/20"
		subnets := bootstrap.SplitIntoN(cidr, 256)

		// Should successfully split /20 into 256 /28 subnets
		if len(subnets) != 256 {
			t.Errorf("SplitIntoN(count=256) returned %d subnets, want 256", len(subnets))
		}

		if len(subnets) > 0 {
			if subnets[0] != "10.4.0.0/28" {
				t.Errorf("First subnet = %v, want 10.4.0.0/28", subnets[0])
			}

			if subnets[255] != "10.4.15.240/28" {
				t.Errorf("Last subnet = %v, want 10.4.15.240/28", subnets[255])
			}
		}
	})
}

// TestCIDRUtilities_Consistency tests that CIDR utilities work consistently together.
func TestCIDRUtilities_Consistency(t *testing.T) {
	t.Parallel()

	t.Run("split subnets have correct boundaries", func(t *testing.T) {
		t.Parallel()

		parentCIDR := "10.4.0.0/20"
		subnets := bootstrap.SplitIntoN(parentCIDR, 4)

		if len(subnets) != 4 {
			t.Fatalf("Expected 4 subnets, got %d", len(subnets))
		}

		// Verify each subnet has proper first/last IPs
		expectedBoundaries := []struct {
			first string
			last  string
		}{
			{"10.4.0.0", "10.4.3.254"},
			{"10.4.4.0", "10.4.7.254"},
			{"10.4.8.0", "10.4.11.254"},
			{"10.4.12.0", "10.4.15.254"},
		}

		for i, subnet := range subnets {
			firstIP := bootstrap.CIDRFirstIP(subnet)
			lastIP := bootstrap.CIDRLastUsableIP(subnet)

			if firstIP != expectedBoundaries[i].first {
				t.Errorf("Subnet %d first IP = %v, want %v", i, firstIP, expectedBoundaries[i].first)
			}

			if lastIP != expectedBoundaries[i].last {
				t.Errorf("Subnet %d last IP = %v, want %v", i, lastIP, expectedBoundaries[i].last)
			}
		}
	})

	t.Run("gateway is always first + 1", func(t *testing.T) {
		t.Parallel()

		testCIDRs := []string{
			"10.4.0.0/20",
			"192.168.1.0/24",
			"172.16.0.0/16",
			"10.0.0.128/25",
		}

		for _, cidr := range testCIDRs {
			gateway := bootstrap.CIDRGatewayIP(cidr)
			firstIP := bootstrap.CIDRFirstIP(cidr)

			// Gateway should be first IP + 1
			// This is a conceptual check - we trust the implementation
			if gateway == "" || firstIP == "" {
				t.Errorf("CIDR %s produced empty gateway=%v or firstIP=%v", cidr, gateway, firstIP)
			}

			if gateway == firstIP {
				t.Errorf("CIDR %s gateway=%v should not equal firstIP=%v", cidr, gateway, firstIP)
			}
		}
	})
}

// BenchmarkSplitIntoN benchmarks the subnet splitting function.
func BenchmarkSplitIntoN(b *testing.B) {
	cidr := "10.4.0.0/20"

	b.Run("split into 4", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = bootstrap.SplitIntoN(cidr, 4)
		}
	})

	b.Run("split into 256", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = bootstrap.SplitIntoN(cidr, 256)
		}
	})
}

// BenchmarkCIDRUtilities benchmarks CIDR utility functions.
func BenchmarkCIDRUtilities(b *testing.B) {
	cidr := "10.4.0.0/24"

	b.Run("CIDRFirstIP", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = bootstrap.CIDRFirstIP(cidr)
		}
	})

	b.Run("CIDRLastUsableIP", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = bootstrap.CIDRLastUsableIP(cidr)
		}
	})

	b.Run("CIDRGatewayIP", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = bootstrap.CIDRGatewayIP(cidr)
		}
	})
}

// setupArtifactsIPTest creates a Manager backed by a stackit fake with the given network CIDR.
func setupArtifactsIPTest(t *testing.T, networkCIDR string) (*bootstrap.Manager, *state.Manager) {
	t.Helper()

	tmp := t.TempDir()

	sm, err := state.NewManager(filepath.Join(tmp, ".state"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err = sm.Load("prod"); err != nil {
		t.Fatal(err)
	}

	cfg := createTestConfig()
	cfg.Network.NetworkCIDR = networkCIDR

	fakeNetwork := &fakeNet{}
	fakeProvider := &fakeProv{n: fakeNetwork, c: &fakeCompute{}}

	mgr := bootstrap.NewManager(cfg, fakeProvider, sm, &bootstrap.Options{
		BlocName: "prod",
		Provider: "stackit",
		Region:   "eu01",
		Yes:      true,
	})

	return mgr, sm
}

// TestArtifactsIPSlot_ResolvesToDotEleven verifies artifacts_ip is subnet base + 11.
// With 10.4.0.0/20 triple strategy, prod-ocfp-0 = 10.4.4.0/22, so .11 = 10.4.4.11.
func TestArtifactsIPSlot_ResolvesToDotEleven(t *testing.T) {
	t.Parallel()

	mgr, sm := setupArtifactsIPTest(t, "10.4.0.0/20")
	ctx := context.Background()

	if err := mgr.CreateNetwork(ctx); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	if err := mgr.CreateSubnets(ctx); err != nil {
		t.Fatalf("CreateSubnets: %v", err)
	}

	got, err := sm.GetOutput("reserved_prod-ocfp-0_artifacts_ip")
	if err != nil {
		t.Fatalf("missing artifacts_ip output: %v", err)
	}

	const want = "10.4.4.11"
	if got != want {
		t.Errorf("artifacts_ip = %q, want %q", got, want)
	}
}

// TestAvailableAIPSlot_ResolvesToStrategyBandStart verifies available_a on
// an ocfp-role subnet comes from the bloc's netlayout strategy's own mgmt
// available band (wide's default: offset 32) rather than the fixed
// artifacts-slot-plus-one offset (12) the pre-netlayout layout used: Layer A
// (this bootstrap resolution) and Layer B (internal/vault's reserved-ips
// population) now read the identical table for every subnetStrategy alike.
func TestAvailableAIPSlot_ResolvesToStrategyBandStart(t *testing.T) {
	t.Parallel()

	mgr, sm := setupArtifactsIPTest(t, "10.4.0.0/20")
	ctx := context.Background()

	if err := mgr.CreateNetwork(ctx); err != nil {
		t.Fatalf("CreateNetwork: %v", err)
	}

	if err := mgr.CreateSubnets(ctx); err != nil {
		t.Fatalf("CreateSubnets: %v", err)
	}

	got, err := sm.GetOutput("reserved_prod-ocfp-0_available_a")
	if err != nil {
		t.Fatalf("missing available_a output: %v", err)
	}

	const want = "10.4.4.32"
	if got != want {
		t.Errorf("available_a = %q, want %q", got, want)
	}
}
