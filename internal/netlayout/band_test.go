package netlayout_test

import (
	"errors"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/netlayout"
)

// TestValidateBand covers wide and compact's shared ValidateBand behavior:
// both strategies delegate to the same internal validateBand helper (see
// internal/netlayout/band.go), a port of the historical
// internal/bootstrap.applyAvailableBandOverride hand-rolled checks, so one
// table exercises both.
func TestValidateBand(t *testing.T) {
	t.Parallel()

	const cidr22 = "10.64.68.0/22" // 1024 addresses (offsets 0-1023), last usable offset 1022

	tests := []struct {
		name    string
		cidr    string
		start   int
		end     int
		wantErr error
	}{
		{name: "start zero end set", cidr: cidr22, start: 0, end: 50, wantErr: netlayout.ErrBandOverridePartial},
		{name: "start set end zero", cidr: cidr22, start: 12, end: 0, wantErr: netlayout.ErrBandOverridePartial},
		{name: "start below floor", cidr: cidr22, start: 5, end: 50, wantErr: netlayout.ErrBandOverrideStartTooLow},
		{name: "end equal to start", cidr: cidr22, start: 100, end: 100, wantErr: netlayout.ErrBandOverrideEndNotAfterStart},
		{name: "end before start", cidr: cidr22, start: 100, end: 50, wantErr: netlayout.ErrBandOverrideEndNotAfterStart},
		{name: "end beyond subnet usable range", cidr: cidr22, start: 12, end: 2000, wantErr: netlayout.ErrBandOverrideEndBeyondSubnet},
		{name: "malformed cidr", cidr: "not-a-cidr", start: 12, end: 50, wantErr: netlayout.ErrInvalidCIDR},
		{name: "valid band", cidr: cidr22, start: 12, end: 50, wantErr: nil},
		// 12 is the historical available-band floor (infraAvailableStart);
		// 1022 is cidr22's last usable offset (1024 addresses - 2 for
		// network/broadcast).
		{name: "valid band at floor and subnet ceiling", cidr: cidr22, start: 12, end: 1022, wantErr: nil},
	}

	for _, strategyName := range []string{"wide", "compact"} {
		strategyName := strategyName

		t.Run(strategyName, func(t *testing.T) {
			t.Parallel()

			layout, err := netlayout.Lookup(strategyName)
			if err != nil {
				t.Fatalf("Lookup(%q) returned unexpected error: %v", strategyName, err)
			}

			for _, tt := range tests {
				tt := tt

				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()

					err := layout.ValidateBand(netlayout.TierInfra, tt.cidr, tt.start, tt.end)

					if tt.wantErr == nil {
						if err != nil {
							t.Fatalf("ValidateBand(%d, %d) = %v, want nil", tt.start, tt.end, err)
						}

						return
					}

					if !errors.Is(err, tt.wantErr) {
						t.Fatalf("ValidateBand(%d, %d) error = %v, want wrapping %v", tt.start, tt.end, err, tt.wantErr)
					}
				})
			}
		})
	}
}
