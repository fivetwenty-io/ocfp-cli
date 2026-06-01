package precompile

import "fmt"

// boshCompiledHost is where cloudfoundry publishes compiled BOSH director
// release tarballs, keyed by stemcell. Confirmed present for noble-1.383.
const boshCompiledHost = "https://s3.amazonaws.com/bosh-compiled-release-tarballs"

// directorReleases is the create-env release set (bosh + bpm). Versions are
// pinned to the latest cloudfoundry/bosh release (v282.1.13 as of 2026-06-01;
// = latest) and the matching bpm. Both have published compiled tarballs for
// noble-1.383, so the director resolves entirely via the fetch-upstream path.
//
// SHAs are intentionally left empty: the create-env pin references the upstream
// compiled URL directly (the artifacts blobstore does not exist at create-env
// time — chicken/egg), and the upstream tarball's integrity is pinned by the
// bosh-deployment manifest the create-env layers under. When BOSHReleases is
// used for the compile-local path the sha is computed during upload.
var directorReleases = []Release{
	{Name: "bosh", Version: "282.1.13"},
	{Name: "bpm", Version: "1.4.31"},
}

// BOSHReleases returns the director release set with upstream compiled URLs
// populated for the given stemcell. For a stemcell with no published compiled
// build, UpstreamCompiledURL is left empty and the release falls to compile-local.
func BOSHReleases(sc Stemcell) []Release {
	out := make([]Release, len(directorReleases))
	for i, r := range directorReleases {
		r.UpstreamCompiledURL = boshCompiledURL(r, sc)
		out[i] = r
	}
	return out
}

// boshCompiledURL builds the published compiled-tarball URL for a director
// release. Only noble stemcells are published in this bucket; non-noble returns
// "" so the caller takes the compile-local path.
func boshCompiledURL(r Release, sc Stemcell) string {
	if sc.OS != "ubuntu-noble" {
		return ""
	}
	return fmt.Sprintf("%s/%s-%s-%s-%s.tgz",
		boshCompiledHost, r.Name, r.Version, sc.OS, sc.Version)
}
