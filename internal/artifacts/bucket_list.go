package artifacts

// CanonicalBucketNames enumerates the bucket names to create on a bloc's
// artifacts S3 endpoint, in a stable order. This is the single source of
// truth for the roster: one bucket per BOSH director (mgmt and ocf/env), and
// four buckets for the CF cloud-controller blobstore (droplets, packages,
// buildpacks, resource-pool). Both the bootstrap create path
// (internal/bootstrap) and the `artifacts provision` command
// (internal/commands) must call this instead of maintaining their own lists,
// so the two provisioning paths never drift out of sync with each other.
func CanonicalBucketNames(blocName string) []string {
	return []string{
		blocName + "-mgmt-bosh",
		blocName + "-ocf-bosh",
		blocName + "-ocf-cf-droplets",
		blocName + "-ocf-cf-packages",
		blocName + "-ocf-cf-buildpacks",
		blocName + "-ocf-cf-resource-pool",
	}
}

// CanonicalBuckets wraps CanonicalBucketNames as []BucketSpec for callers
// (EnsureBuckets) that need the spec shape rather than plain strings.
func CanonicalBuckets(blocName string) []BucketSpec {
	names := CanonicalBucketNames(blocName)
	specs := make([]BucketSpec, 0, len(names))

	for _, name := range names {
		specs = append(specs, BucketSpec{Name: name})
	}

	return specs
}
