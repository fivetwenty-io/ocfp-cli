package config

import "testing"

func TestGCPZoneParity(t *testing.T) {
	for _, region := range []string{"us-central1", "us-central1-a"} {
		if got := FormatAvailabilityZone("gcp", region, 1); got != "us-central1-b" {
			t.Errorf("%s: got %s", region, got)
		}
	}
}
