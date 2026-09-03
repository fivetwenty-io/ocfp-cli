package artifacts

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

const (
	bucketProbeTimeout = 30 * time.Second
)

// BucketSpec names a bucket to create on the artifacts S3 endpoint.
type BucketSpec struct {
	Name string
}

// EnsureBuckets is idempotent: each bucket is created if absent, ignored if
// already owned by the caller. Returns the first non-recoverable error.
//
// The S3 client is built per-call so this helper has no shared state. Suitable
// for both the bootstrap step and the readiness probe path.
func EnsureBuckets(ctx context.Context, ep Endpoint, creds Credentials, buckets []BucketSpec) error {
	cli, err := newS3Client(ep, creds)
	if err != nil {
		return err
	}

	for _, b := range buckets {
		err := ensureBucket(ctx, cli, b.Name)
		if err != nil {
			return fmt.Errorf("bucket %s: %w", b.Name, err)
		}
	}

	return nil
}

// Probe verifies the endpoint is responding by issuing a ListBuckets call.
// Used as the readiness gate after VM creation.
func Probe(ctx context.Context, ep Endpoint, creds Credentials) error {
	cli, err := newS3Client(ep, creds)
	if err != nil {
		return err
	}

	probeCtx, cancel := context.WithTimeout(ctx, bucketProbeTimeout)
	defer cancel()

	_, err = cli.ListBuckets(probeCtx, &s3.ListBucketsInput{})
	if err != nil {
		return fmt.Errorf("probing artifacts endpoint: %w", err)
	}

	return nil
}

// NewS3Client builds an AWS SDK v2 S3 client for the artifacts RustFS endpoint,
// applying the same path-style + CA/SkipTLSVerify TLS matrix used internally for
// bucket creation. Exported so other packages (e.g. precompile) can reuse the
// exact RustFS-compatible client without duplicating the TLS logic.
func NewS3Client(ep Endpoint, creds Credentials) (*s3.Client, error) {
	return newS3Client(ep, creds)
}

func newS3Client(ep Endpoint, creds Credentials) (*s3.Client, error) {
	var httpClient *http.Client

	switch {
	case ep.CACert != "":
		// Operator supplied a CA bundle — pin to that pool.
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}

		pool, _ := x509.SystemCertPool()
		if pool == nil {
			pool = x509.NewCertPool()
		}

		if !pool.AppendCertsFromPEM([]byte(ep.CACert)) {
			return nil, ErrCACertNoPEM
		}

		tlsCfg.RootCAs = pool
		httpClient = &http.Client{
			Transport: &http.Transport{TLSClientConfig: tlsCfg, Proxy: http.ProxyFromEnvironment},
			Timeout:   bucketProbeTimeout,
		}

	case ep.SkipTLSVerify:
		// Operator explicitly opted out of verification (e.g. RustFS self-signed without CA).
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} // #nosec G402 -- operator-controlled; SkipTLSVerify must be set explicitly
		httpClient = &http.Client{
			Transport: &http.Transport{TLSClientConfig: tlsCfg, Proxy: http.ProxyFromEnvironment},
			Timeout:   bucketProbeTimeout,
		}

	default:
		// No CA and no skip flag — use system TLS defaults.
		httpClient = &http.Client{
			Transport: &http.Transport{Proxy: http.ProxyFromEnvironment},
			Timeout:   bucketProbeTimeout,
		}
	}

	region := ep.Region
	if region == "" {
		region = "us-east-1"
	}

	awsCfg := aws.Config{
		Region:       region,
		Credentials:  aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(creds.AccessKey, creds.SecretKey, "")),
		HTTPClient:   httpClient,
		BaseEndpoint: aws.String(ep.URL),
	}

	cli := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = ep.PathStyle
		o.BaseEndpoint = aws.String(ep.URL)
	})

	return cli, nil
}

// ensureBucket creates a bucket idempotently. Already-owned buckets are not
// an error; foreign-owned bucket names short-circuit with the original error.
func ensureBucket(ctx context.Context, cli *s3.Client, name string) error {
	_, err := cli.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(name)})
	if err == nil {
		return nil
	}

	var (
		alreadyExists     *s3types.BucketAlreadyExists
		alreadyOwnedByYou *s3types.BucketAlreadyOwnedByYou
		apiErr            smithy.APIError
	)

	switch {
	case errors.As(err, &alreadyOwnedByYou):
		return nil
	case errors.As(err, &alreadyExists):
		return fmt.Errorf("bucket name taken by another account: %w", err)
	case errors.As(err, &apiErr) && apiErr.ErrorCode() == "BucketAlreadyOwnedByYou":
		return nil
	}

	return fmt.Errorf("creating bucket %q: %w", name, err)
}
