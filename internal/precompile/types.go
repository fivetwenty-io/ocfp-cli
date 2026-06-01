// Package precompile populates the artifacts blobstore with compiled BOSH
// release tarballs and emits ops files that pin deployment releases to those
// tarballs, so create-env and CF deploys skip source compilation.
//
// Per-release resolution mirrors the reference store-ops pipeline in
// bosh-pve-cpi-release/scripts (_compiled.py, _cfcompile.py):
//
//  1. present?       — the compiled tarball already exists in the blobstore for
//     this stemcell, reuse it.
//  2. fetch-upstream — a published compiled tarball exists for this stemcell,
//     stream it into the blobstore.
//  3. compile-local  — no upstream compiled build exists, compile on the
//     director (no-VM deploy + export-release) and stream the result in.
package precompile

// Target selects the release set to precompile.
type Target string

const (
	// TargetBOSH is the director release set (bosh + bpm), consumed by create-env.
	TargetBOSH Target = "bosh"
	// TargetCF is the cf-deployment release set, consumed by `genesis deploy cf`.
	TargetCF Target = "cf"
)

// Stemcell identifies the OS + version a compiled release is built against.
// Compiled packages are exact-stemcell-locked: a deploy whose stemcell differs
// from the compiled set's stemcell forces a full recompile (or fails).
type Stemcell struct {
	OS      string
	Version string
}

// DefaultStemcell is the single stemcell standardized across the director and
// CF (locked decision). Director compiled releases are published upstream for
// this version; CF releases are compiled locally once and cached.
var DefaultStemcell = Stemcell{OS: "ubuntu-noble", Version: "1.383"}

// String renders the stemcell as "os/version" (e.g. "ubuntu-noble/1.383").
func (s Stemcell) String() string { return s.OS + "/" + s.Version }

// Release is a single BOSH release to precompile, with its upstream source.
// UpstreamCompiledURL is non-empty when a published compiled tarball exists for
// the target stemcell (fetch-upstream path); empty forces the compile-local
// path. UpstreamSourceURL + UpstreamSourceSHA locate the source tarball that
// must be uploaded to the director before a local compile can run.
type Release struct {
	Name                string
	Version             string
	UpstreamSourceURL   string
	UpstreamSourceSHA   string
	UpstreamCompiledURL string
	UpstreamCompiledSHA string
}

// Source records how a release was resolved, for reporting + dry-run output.
type Source string

const (
	// SourcePresent means the compiled tarball already existed in the blobstore.
	SourcePresent Source = "present"
	// SourceUpstream means the compiled tarball was fetched from upstream.
	SourceUpstream Source = "upstream"
	// SourceCompiled means the tarball was compiled locally on the director.
	SourceCompiled Source = "compiled"
)

// Resolution is the outcome of resolving one release: where the compiled tarball
// lives (URL the director will fetch from) and its sha for the pin ops file.
type Resolution struct {
	Release
	Source Source
	// URL the deploying director fetches the compiled tarball from. For CF this
	// is the RustFS path-style https URL; for the director it is the upstream
	// compiled URL (the artifacts VM does not yet exist at create-env time).
	URL string
	// SHA carries the algorithm prefix ("sha256:...") expected by bosh.
	SHA string
}

// Options tune a precompile run.
type Options struct {
	// Force rebuilds/re-uploads even when a tarball is already present.
	Force bool
	// DryRun resolves and reports the per-release plan without mutating the blobstore.
	DryRun bool
	// Concurrency caps parallel export+upload work (compile-local path). <=0 means 1.
	Concurrency int
	// Stemcell overrides DefaultStemcell; zero value falls back to DefaultStemcell.
	Stemcell Stemcell
}

// stemcell returns the effective stemcell for the options.
func (o Options) stemcell() Stemcell {
	if o.Stemcell.OS != "" && o.Stemcell.Version != "" {
		return o.Stemcell
	}
	return DefaultStemcell
}

// concurrency returns the effective worker count (>=1).
func (o Options) concurrency() int {
	if o.Concurrency < 1 {
		return 1
	}
	return o.Concurrency
}
