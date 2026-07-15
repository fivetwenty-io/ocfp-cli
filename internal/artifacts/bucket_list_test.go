package artifacts

import "testing"

// TestCanonicalBucketNames_ContainsAllRequiredBuckets pins the bucket roster
// shared by the bootstrap create path and the `artifacts provision` command.
// Dropping any of these silently breaks one of:
//   - mgmt-bosh blobstore (mgmt director releases/stemcells)
//   - ocf-bosh blobstore (env-BOSH director releases/stemcells)
//   - CF cloud-controller blobstore (droplets/packages/buildpacks/resources)
func TestCanonicalBucketNames_ContainsAllRequiredBuckets(t *testing.T) {
	t.Parallel()

	got := CanonicalBucketNames("ocfp-lab-wayne")

	want := map[string]bool{
		"ocfp-lab-wayne-mgmt-bosh":            true,
		"ocfp-lab-wayne-ocf-bosh":             true,
		"ocfp-lab-wayne-ocf-cf-droplets":      true,
		"ocfp-lab-wayne-ocf-cf-packages":      true,
		"ocfp-lab-wayne-ocf-cf-buildpacks":    true,
		"ocfp-lab-wayne-ocf-cf-resource-pool": true,
	}

	if len(got) != len(want) {
		t.Fatalf("CanonicalBucketNames returned %d buckets, want %d: %+v", len(got), len(want), got)
	}

	seen := map[string]bool{}

	for _, name := range got {
		if !want[name] {
			t.Errorf("unexpected bucket in list: %s", name)
		}

		if seen[name] {
			t.Errorf("duplicate bucket in list: %s", name)
		}

		seen[name] = true
	}

	for name := range want {
		if !seen[name] {
			t.Errorf("required bucket missing from list: %s", name)
		}
	}
}

// TestCanonicalBuckets_MatchesCanonicalBucketNames asserts CanonicalBuckets is
// a pure BucketSpec wrapper over CanonicalBucketNames — the two call sites
// (bootstrap wants []BucketSpec, the provision command wants []string) must
// never be able to drift relative to each other.
func TestCanonicalBuckets_MatchesCanonicalBucketNames(t *testing.T) {
	t.Parallel()

	names := CanonicalBucketNames("test-bloc")
	specs := CanonicalBuckets("test-bloc")

	if len(specs) != len(names) {
		t.Fatalf("CanonicalBuckets returned %d specs, want %d matching CanonicalBucketNames", len(specs), len(names))
	}

	for i, spec := range specs {
		if spec.Name != names[i] {
			t.Errorf("CanonicalBuckets[%d].Name = %q, want %q", i, spec.Name, names[i])
		}
	}
}

// TestCanonicalBucketNames_EmptyBlocName documents the (degenerate but
// non-crashing) behavior for an empty bloc name: names collapse to bare
// suffixes rather than panicking. Callers are expected to validate blocName
// is non-empty before reaching here (state/config load already enforce this).
func TestCanonicalBucketNames_EmptyBlocName(t *testing.T) {
	t.Parallel()

	got := CanonicalBucketNames("")
	if len(got) != 6 {
		t.Fatalf("CanonicalBucketNames(\"\") returned %d entries, want 6", len(got))
	}

	if got[0] != "-mgmt-bosh" {
		t.Errorf("CanonicalBucketNames(\"\")[0] = %q, want %q", got[0], "-mgmt-bosh")
	}
}
