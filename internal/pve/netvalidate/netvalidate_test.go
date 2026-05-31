package netvalidate_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/pve/netvalidate"
)

// T26 TestValidateNetworkPairing_MatchingCIDRs_OK verifies that identical
// CIDRs — including representations with host bits set — return nil.
func TestValidateNetworkPairing_MatchingCIDRs_OK(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name              string
		directorCIDR      string
		cfCloudConfigCIDR string
	}{
		{
			name:              "exact same canonical CIDR",
			directorCIDR:      "10.64.64.0/18",
			cfCloudConfigCIDR: "10.64.64.0/18",
		},
		{
			name:              "host bits set in director CIDR normalizes equal",
			directorCIDR:      "10.64.64.10/18",
			cfCloudConfigCIDR: "10.64.64.0/18",
		},
		{
			name:              "host bits set in both normalize equal",
			directorCIDR:      "10.64.64.10/18",
			cfCloudConfigCIDR: "10.64.64.50/18",
		},
		{
			name:              "class C match",
			directorCIDR:      "192.168.1.0/24",
			cfCloudConfigCIDR: "192.168.1.0/24",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := netvalidate.ValidateNetworkPairing(tc.directorCIDR, tc.cfCloudConfigCIDR)
			if err != nil {
				t.Errorf("expected nil error for matching CIDRs (%q, %q), got: %v",
					tc.directorCIDR, tc.cfCloudConfigCIDR, err)
			}
		})
	}
}

// T27 TestValidateNetworkPairing_MismatchedCIDRs_ReturnsError verifies that a
// mismatch returns an error that wraps ErrCIDRMismatch and contains both
// "tailscale" and "LAN route" in the message (the hazard explanation).
func TestValidateNetworkPairing_MismatchedCIDRs_ReturnsError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name              string
		directorCIDR      string
		cfCloudConfigCIDR string
	}{
		{
			name:              "default 192.168.1/24 vs lab 10.64.64/18",
			directorCIDR:      "192.168.1.0/24",
			cfCloudConfigCIDR: "10.64.64.0/18",
		},
		{
			name:              "10.0.0.0/8 vs 10.0.0.0/24 — different prefix length",
			directorCIDR:      "10.0.0.0/8",
			cfCloudConfigCIDR: "10.0.0.0/24",
		},
		{
			name:              "totally different networks",
			directorCIDR:      "172.16.0.0/16",
			cfCloudConfigCIDR: "10.64.64.0/18",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := netvalidate.ValidateNetworkPairing(tc.directorCIDR, tc.cfCloudConfigCIDR)
			if err == nil {
				t.Fatalf("expected error for mismatched CIDRs (%q, %q), got nil",
					tc.directorCIDR, tc.cfCloudConfigCIDR)
			}

			if !errors.Is(err, netvalidate.ErrCIDRMismatch) {
				t.Errorf("expected error to wrap ErrCIDRMismatch, got: %v", err)
			}

			msg := err.Error()

			if !strings.Contains(strings.ToLower(msg), "tailscale") {
				t.Errorf("error message should contain 'tailscale'; got: %s", msg)
			}

			if !strings.Contains(strings.ToLower(msg), "lan route") {
				t.Errorf("error message should contain 'LAN route'; got: %s", msg)
			}

			if !strings.Contains(msg, tc.directorCIDR) {
				t.Errorf("error message should contain director CIDR %q; got: %s", tc.directorCIDR, msg)
			}

			if !strings.Contains(msg, tc.cfCloudConfigCIDR) {
				t.Errorf("error message should contain cf CIDR %q; got: %s", tc.cfCloudConfigCIDR, msg)
			}
		})
	}
}

// T28 TestValidateNetworkPairing_EmptyInputs_SkipsValidation verifies that
// empty CIDR strings return an argument error (not a mismatch error).
// The "skip when incomplete" contract is enforced by the caller (config.validate)
// which guards both fields before calling ValidateNetworkPairing.
func TestValidateNetworkPairing_EmptyInputs_SkipsValidation(t *testing.T) {
	t.Parallel()

	t.Run("both empty returns argument error not mismatch", func(t *testing.T) {
		t.Parallel()
		err := netvalidate.ValidateNetworkPairing("", "")
		if err == nil {
			t.Fatal("expected error when both CIDRs are empty, got nil")
		}
		if errors.Is(err, netvalidate.ErrCIDRMismatch) {
			t.Errorf("expected argument error, not ErrCIDRMismatch, got: %v", err)
		}
	})

	t.Run("director empty returns argument error", func(t *testing.T) {
		t.Parallel()
		err := netvalidate.ValidateNetworkPairing("", "10.64.64.0/18")
		if err == nil {
			t.Fatal("expected error when director CIDR is empty, got nil")
		}
		if errors.Is(err, netvalidate.ErrCIDRMismatch) {
			t.Errorf("expected argument error, not ErrCIDRMismatch, got: %v", err)
		}
	})

	t.Run("cf cloud-config empty returns argument error", func(t *testing.T) {
		t.Parallel()
		err := netvalidate.ValidateNetworkPairing("10.64.64.0/18", "")
		if err == nil {
			t.Fatal("expected error when cf CIDR is empty, got nil")
		}
		if errors.Is(err, netvalidate.ErrCIDRMismatch) {
			t.Errorf("expected argument error, not ErrCIDRMismatch, got: %v", err)
		}
	})
}

// TestValidateNetworkPairing_ParseErrors verifies that invalid CIDR strings
// return parse errors, not mismatch errors.
func TestValidateNetworkPairing_ParseErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name              string
		directorCIDR      string
		cfCloudConfigCIDR string
	}{
		{
			name:              "invalid director CIDR",
			directorCIDR:      "not-a-cidr",
			cfCloudConfigCIDR: "10.64.64.0/18",
		},
		{
			name:              "invalid cf cloud-config CIDR",
			directorCIDR:      "10.64.64.0/18",
			cfCloudConfigCIDR: "not-a-cidr",
		},
		{
			name:              "bare IP without prefix",
			directorCIDR:      "10.64.64.0",
			cfCloudConfigCIDR: "10.64.64.0/18",
		},
		{
			name:              "prefix out of range",
			directorCIDR:      "10.64.64.0/99",
			cfCloudConfigCIDR: "10.64.64.0/18",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := netvalidate.ValidateNetworkPairing(tc.directorCIDR, tc.cfCloudConfigCIDR)
			if err == nil {
				t.Fatalf("expected parse error for invalid CIDR input, got nil")
			}
			if errors.Is(err, netvalidate.ErrCIDRMismatch) {
				t.Errorf("expected parse error, not ErrCIDRMismatch, for invalid CIDR input; got: %v", err)
			}
		})
	}
}
