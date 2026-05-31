package bootstrap

import (
	"testing"
)

// TestArtifactsBucketList_ContainsAllRequiredBuckets pins the bucket roster
// the artifacts step provisions on the RustFS endpoint. Dropping any of
// these silently breaks one of:
//   - mgmt-bosh blobstore (mgmt director releases/stemcells)
//   - ocf-bosh blobstore (env-BOSH director releases/stemcells)
//   - CF cloud-controller blobstore (droplets/packages/buildpacks/resources)
func TestArtifactsBucketList_ContainsAllRequiredBuckets(t *testing.T) {
	t.Parallel()

	got := artifactsBucketList("ocfp-lab-wayne", nil)

	want := map[string]bool{
		"ocfp-lab-wayne-mgmt-bosh":            true,
		"ocfp-lab-wayne-ocf-bosh":             true,
		"ocfp-lab-wayne-ocf-cf-droplets":      true,
		"ocfp-lab-wayne-ocf-cf-packages":      true,
		"ocfp-lab-wayne-ocf-cf-buildpacks":    true,
		"ocfp-lab-wayne-ocf-cf-resource-pool": true,
	}

	if len(got) != len(want) {
		t.Fatalf("artifactsBucketList returned %d buckets, want %d: %+v", len(got), len(want), got)
	}

	seen := map[string]bool{}

	for _, b := range got {
		if !want[b.Name] {
			t.Errorf("unexpected bucket in list: %s", b.Name)
		}

		if seen[b.Name] {
			t.Errorf("duplicate bucket in list: %s", b.Name)
		}

		seen[b.Name] = true
	}

	for name := range want {
		if !seen[name] {
			t.Errorf("required bucket missing from list: %s", name)
		}
	}
}
