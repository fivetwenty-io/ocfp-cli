package precompile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// shaMetaKey is the user-metadata key under which the compiled tarball's
// sha256 is stored, so a warm run can rebuild the pin without re-downloading.
const shaMetaKey = "sha256"

// shaPrefix is the algorithm prefix bosh expects in a release sha1 field for a
// sha256 digest.
const shaPrefix = "sha256:"

// objectAPI is the subset of the S3 client used here, narrowed for testing.
type objectAPI interface {
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, opts ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// HeadCompiled returns the stored sha (with "sha256:" prefix) for an object,
// and whether it exists. A missing object returns ok=false, err=nil.
func HeadCompiled(ctx context.Context, cli objectAPI, bucket, key string) (sha string, ok bool, err error) {
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

	if h, present := out.Metadata[shaMetaKey]; present && h != "" {
		return shaPrefix + h, true, nil
	}
	// Object exists but has no recorded sha (e.g. uploaded out-of-band).
	return "", true, nil
}

// UploadCompiledFile uploads a tarball from disk to the blobstore, recording its
// sha256 as object metadata. Returns the sha with the "sha256:" prefix. Reads
// the file twice is avoided: the sha is computed in a first streaming pass, then
// the seekable file is handed to PutObject for the body.
func UploadCompiledFile(ctx context.Context, cli objectAPI, bucket, key, path string) (string, error) {
	hexSHA, err := fileSHA256(path)
	if err != nil {
		return "", err
	}

	f, err := os.Open(path) //nolint:gosec // path is a temp file produced by export-release/download
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	_, err = cli.PutObject(ctx, &s3.PutObjectInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		Body:     f,
		Metadata: map[string]string{shaMetaKey: hexSHA},
	})
	if err != nil {
		return "", fmt.Errorf("put %s/%s: %w", bucket, key, err)
	}

	return shaPrefix + hexSHA, nil
}

// DownloadToFile streams a URL to destPath, returning the sha256 (hex, no prefix)
// computed during the transfer. Used for the fetch-upstream path before upload.
func DownloadToFile(ctx context.Context, hc *http.Client, url, destPath string) (string, error) {
	if hc == nil {
		hc = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("request %s: %w", url, err)
	}

	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("get %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get %s: status %d", url, resp.StatusCode)
	}

	f, err := os.Create(destPath) //nolint:gosec // destPath is a caller-controlled temp path
	if err != nil {
		return "", fmt.Errorf("create %s: %w", destPath, err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return "", fmt.Errorf("download %s: %w", url, err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// RemoteSHA256 streams a URL through a sha256 digest without persisting it,
// returning the sha with the "sha256:" prefix. Used to pin a director release
// directly to its upstream compiled URL (no blobstore round-trip).
func RemoteSHA256(ctx context.Context, hc *http.Client, url string) (string, error) {
	if hc == nil {
		hc = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("request %s: %w", url, err)
	}

	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("get %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get %s: status %d", url, resp.StatusCode)
	}

	h := sha256.New()
	if _, err := io.Copy(h, resp.Body); err != nil {
		return "", fmt.Errorf("hashing %s: %w", url, err)
	}

	return shaPrefix + hex.EncodeToString(h.Sum(nil)), nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // path is a temp file produced by export-release/download
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hashing %s: %w", path, err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

func isNotFound(err error) bool {
	var nsk *s3types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}

	var nf *s3types.NotFound
	if errors.As(err, &nf) {
		return true
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()

		return code == "NotFound" || code == "NoSuchKey" || code == "404"
	}

	return false
}
