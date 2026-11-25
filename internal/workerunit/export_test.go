package workerunit

// ExtractBundleWithin reads a bundle held to smaller bounds than the join
// contract's, so a test can pass an archive or a tar stream past its bound
// without writing hundreds of MiB.
func ExtractBundleWithin(archive, dir, arch string, binary, join, whole int64) (Bundle, error) {
	return extractBundle(archive, dir, arch, bundleBounds{binary: binary, join: join, archive: whole})
}

// UnsafeName reports whether the reader refuses a member's name as one
// that could leave its directory.
var UnsafeName = unsafeName

// ContractBounds are the join contract's bounds on the binary, the join
// file, and the whole archive.
var ContractBounds = [3]int64{maxBinary, maxJoinFile, maxBundle}
