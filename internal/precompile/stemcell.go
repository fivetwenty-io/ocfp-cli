package precompile

import (
	"context"
	"crypto/sha1" // #nosec G505 -- sha1 verifies against the upstream-published bosh.io/GCS digest, not used for cryptographic security
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// stemcellKeyPrefix is the blobstore key prefix for cached stemcell tarballs.
const stemcellKeyPrefix = "stemcells"

// StemcellKey returns the blobstore object key for a cached stemcell tarball
// (e.g. stemcells/ubuntu-noble-1.383.tgz). Unlike CompiledKey, no OS/stemcell
// pin is folded in beyond the stemcell's own name+version: a stemcell tarball
// is not compiled against another stemcell, so there is no cross-stemcell
// collision to guard against.
func StemcellKey(name, version string) string {
	return fmt.Sprintf("%s/%s-%s.tgz", stemcellKeyPrefix, slug(name), slug(version))
}

// ResolveStemcell ensures a stemcell tarball is cached in the RustFS
// blobstore, downloading it from upstreamURL and uploading it to the cache on
// a miss (or when force is set), and returns the RustFS path-style https URL
// callers should hand to `bosh upload-stemcell` plus the tarball's sha256
// (hex, no algorithm prefix).
//
// This resolver is intentionally independent of blobstore.go's
// HeadCompiled/UploadCompiledFile: those exist for the CF compiled-release
// path, which is keyed and reasoned about in terms of a sha256 pin consumed
// directly by a release ops file. Stemcells are pinned by
// `bosh upload-stemcell --sha1`, whose sha1 is sourced by the caller from the
// existing SHA1Fetcher, not from this function — the sha256 returned here is
// only a cache-integrity value, never handed to bosh.
//
// Inputs: name/version identify the stemcell (e.g. "ubuntu-noble", "1.383")
// and must be non-empty. upstreamURL is the fetch source used on a cache
// miss; it may be empty only when the tarball is already cached (force=false
// and a prior upload exists) — an empty upstreamURL on a genuine cache miss
// is a caller error and returns an error without contacting S3 again.
// expectedSHA1 is the upstream-published sha1 (e.g. from SHA1Fetcher) to
// verify the downloaded tarball against before it is cached; when empty, no
// sha1 check is performed (a cache-warm caller may not have a pin at hand).
// The present-check hit path (force=false, object already cached) never
// downloads, so expectedSHA1 is ignored on that path.
//
// Failure modes: empty name or version returns an error before any I/O. An
// S3 HeadObject failure other than not-found returns a wrapped error. A
// cache miss (or force=true) with no upstreamURL returns an error. A
// download failure (network error or non-200 status) returns a wrapped
// error and leaves the cache untouched. A non-empty expectedSHA1 that does
// not match the downloaded tarball's sha1 returns an error naming both
// digests; nothing is uploaded and the temp file is cleaned up. An S3
// PutObject failure returns a wrapped error; the downloaded temp file is
// always cleaned up regardless of outcome. A present cache hit with no
// recorded sha256 metadata (object uploaded out-of-band) returns ok with an
// empty sha256 string, not an error.
func ResolveStemcell(ctx context.Context, cli objectAPI, hc *http.Client, bucket, endpoint, name, version, upstreamURL, expectedSHA1 string, force bool) (url, sha256hex string, err error) {
	if name == "" {
		return "", "", errors.New("resolve stemcell: name is required")
	}

	if version == "" {
		return "", "", fmt.Errorf("resolve stemcell %s: version is required", name)
	}

	key := StemcellKey(name, version)
	url = HTTPSURL(endpoint, bucket, key)

	if !force {
		sha, ok, headErr := headStemcell(ctx, cli, bucket, key)
		if headErr != nil {
			return "", "", headErr
		}

		if ok {
			return url, sha, nil
		}
	}

	if upstreamURL == "" {
		return "", "", fmt.Errorf("resolve stemcell %s/%s: not cached in %s/%s and no upstream URL to fetch from", name, version, bucket, key)
	}

	workDir, err := os.MkdirTemp("", "ocfp-stemcell-")
	if err != nil {
		return "", "", fmt.Errorf("resolve stemcell %s/%s: mktemp: %w", name, version, err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	tmp := filepath.Join(workDir, slug(name)+"-"+slug(version)+".tgz")

	sha, gotSHA1, err := downloadStemcellFile(ctx, hc, upstreamURL, tmp)
	if err != nil {
		return "", "", fmt.Errorf("resolve stemcell %s/%s: %w", name, version, err)
	}

	if expectedSHA1 != "" && !strings.EqualFold(gotSHA1, expectedSHA1) {
		return "", "", fmt.Errorf("resolve stemcell %s/%s: sha1 mismatch: upstream reported %s, downloaded tarball has %s", name, version, expectedSHA1, gotSHA1)
	}

	if err := uploadStemcell(ctx, cli, bucket, key, tmp, sha); err != nil {
		return "", "", fmt.Errorf("resolve stemcell %s/%s: %w", name, version, err)
	}

	return url, sha, nil
}

// downloadStemcellFile streams url to destPath, returning the sha256 (hex, no
// prefix) and sha1 (hex, no prefix) computed in the same pass over the
// response body. A local helper rather than a reuse of DownloadToFile: the
// sha1 verification this resolver needs is specific to stemcells (bosh.io
// publishes sha1 for stemcells, not sha256) and DownloadToFile has no sha1
// output.
func downloadStemcellFile(ctx context.Context, hc *http.Client, url, destPath string) (sha256hex, sha1hex string, err error) {
	if hc == nil {
		hc = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", fmt.Errorf("request %s: %w", url, err)
	}

	resp, err := hc.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("get %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("get %s: status %d", url, resp.StatusCode)
	}

	f, err := os.Create(destPath) // #nosec G304 -- destPath is a caller-controlled temp path
	if err != nil {
		return "", "", fmt.Errorf("create %s: %w", destPath, err)
	}
	defer func() { _ = f.Close() }()

	h256 := sha256.New()

	h1 := sha1.New() // #nosec G401 -- sha1 verifies against the upstream-published bosh.io/GCS digest, not used for cryptographic security
	if _, err := io.Copy(io.MultiWriter(f, h256, h1), resp.Body); err != nil {
		return "", "", fmt.Errorf("download %s: %w", url, err)
	}

	return hex.EncodeToString(h256.Sum(nil)), hex.EncodeToString(h1.Sum(nil)), nil
}

// headStemcell reports whether a stemcell tarball is already cached, and its
// recorded sha256 (hex, no prefix) if any. A missing object returns
// ok=false, err=nil, mirroring HeadCompiled's not-found contract.
func headStemcell(ctx context.Context, cli objectAPI, bucket, key string) (sha string, ok bool, err error) {
	out, err := cli.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return "", false, nil
		}

		return "", false, fmt.Errorf("head %s/%s: %w", bucket, key, err)
	}

	return out.Metadata[shaMetaKey], true, nil
}

// uploadStemcell uploads a stemcell tarball from disk to the blobstore,
// recording its sha256 (hex, no prefix) as object metadata for later cache
// verification by headStemcell.
func uploadStemcell(ctx context.Context, cli objectAPI, bucket, key, path, sha256hex string) error {
	f, err := os.Open(path) // #nosec G304 -- path is a temp file this package created and controls
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	_, err = cli.PutObject(ctx, &s3.PutObjectInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		Body:     f,
		Metadata: map[string]string{shaMetaKey: sha256hex},
	})
	if err != nil {
		return fmt.Errorf("put %s/%s: %w", bucket, key, err)
	}

	return nil
}
