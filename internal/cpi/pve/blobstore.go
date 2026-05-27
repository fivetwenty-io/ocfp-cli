package pve

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

const (
	// blobstoreDeleteBatchSize is the max objects per DeleteObjects S3 call.
	blobstoreDeleteBatchSize = 1000
	// blobstoreLifecycleRuleID is the rule ID used for noncurrent expiration.
	blobstoreLifecycleRuleID = "DeleteOldObjects"
)

// blobstoreS3Client wraps an aws-sdk-go-v2 S3 client configured against the
// operator's S3-compatible endpoint (Ceph RGW, RustFS, etc.). The wrapper is
// built lazily on first use so local-mode deployments never instantiate the
// SDK or touch a network.
type blobstoreS3Client struct {
	cli    *s3.Client
	region string
}

// blobstoreClient lazily constructs the S3 client. Returns nil for local mode.
//
// Local-mode callers do not need to handle errors from this — SupportsStorage
// already returns false there, so the bootstrap bucket path is skipped before
// any of the bucket methods get invoked.
func (m *StorageManager) blobstoreClient() (*blobstoreS3Client, error) {
	if m.client == nil || m.client.config == nil {
		return nil, ErrBucketsNotSupported
	}

	cfg := m.client.config
	if !cfg.isExternalBlobstore() {
		return nil, ErrBucketsNotSupported
	}

	m.blobstoreMu.Lock()
	defer m.blobstoreMu.Unlock()

	if m.blobstoreS3 != nil {
		return m.blobstoreS3, nil
	}

	region := cfg.BlobstoreRegion
	if region == "" {
		region = "us-east-1"
	}

	httpClient, err := buildBlobstoreHTTPClient(cfg.BlobstoreCAPath, !cfg.VerifySSL)
	if err != nil {
		return nil, fmt.Errorf("blobstore: build http client: %w", err)
	}

	awsCfg := aws.Config{
		Region:                      region,
		Credentials:                 aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider(cfg.BlobstoreAccessKey, cfg.BlobstoreSecretKey, "")),
		BearerAuthTokenProvider:     nil,
		HTTPClient:                  httpClient,
		EndpointResolver:            nil,
		EndpointResolverWithOptions: nil,
		RetryMaxAttempts:            0,
		RetryMode:                   "",
		Retryer:                     nil,
		ConfigSources:               nil,
		APIOptions:                  nil,
		Logger:                      nil,
		ClientLogMode:               0,
		DefaultsMode:                "",
		RuntimeEnvironment: aws.RuntimeEnvironment{
			EnvironmentIdentifier:     "",
			Region:                    "",
			EC2InstanceMetadataRegion: "",
		},
		AppID:                       "",
		BaseEndpoint:                aws.String(cfg.BlobstoreEndpoint),
		DisableRequestCompression:   false,
		RequestMinCompressSizeBytes: 0,
		AccountIDEndpointMode:       aws.AccountIDEndpointModeDisabled,
		RequestChecksumCalculation:  aws.RequestChecksumCalculationWhenSupported,
		ResponseChecksumValidation:  aws.ResponseChecksumValidationWhenSupported,
		Interceptors: smithyhttp.InterceptorRegistry{
			BeforeExecution:       nil,
			BeforeSerialization:   nil,
			AfterSerialization:    nil,
			BeforeRetryLoop:       nil,
			BeforeAttempt:         nil,
			BeforeSigning:         nil,
			AfterSigning:          nil,
			BeforeTransmit:        nil,
			AfterTransmit:         nil,
			BeforeDeserialization: nil,
			AfterDeserialization:  nil,
			AfterAttempt:          nil,
			AfterExecution:        nil,
		},
		AuthSchemePreference: nil,
		ServiceOptions:       nil,
	}

	s3cli := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = cfg.BlobstorePathStyle
		o.BaseEndpoint = aws.String(cfg.BlobstoreEndpoint)
	})

	m.blobstoreS3 = &blobstoreS3Client{cli: s3cli, region: region}

	return m.blobstoreS3, nil
}

// isExternalBlobstore reports whether the config selects external (S3) mode.
func (c *Config) isExternalBlobstore() bool {
	if c == nil {
		return false
	}

	return c.BlobstoreMode == "external"
}

// buildBlobstoreHTTPClient produces an *http.Client matching the operator's
// TLS preferences. caPath, when set, augments the system roots with a custom
// bundle (Ceph clusters often present a self-signed cert chain).
func buildBlobstoreHTTPClient(caPath string, insecure bool) (*http.Client, error) {
	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: insecure, //nolint:gosec // operator-controlled flag
	}

	if caPath != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}

		pem, err := os.ReadFile(caPath) //nolint:gosec // operator-supplied path
		if err != nil {
			return nil, fmt.Errorf("read CA bundle %s: %w", caPath, err)
		}

		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("CA bundle %s: no certificates parsed", caPath)
		}

		tlsConfig.RootCAs = pool
	}

	transport := &http.Transport{
		TLSClientConfig: tlsConfig,
		Proxy:           http.ProxyFromEnvironment,
	}

	return &http.Client{Transport: transport, Timeout: 60 * time.Second}, nil
}

// createBucketExternal creates a bucket via the S3-compatible API.
func (m *StorageManager) createBucketExternal(ctx context.Context, req *cpi.BucketRequest) (*cpi.Bucket, error) {
	cli, err := m.blobstoreClient()
	if err != nil {
		return nil, err
	}

	op := logger.WithOperation("CreateBucket")
	op.Infof("Creating bucket via external blobstore: %s", req.Name)

	_, err = cli.cli.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(req.Name),
	})
	if err != nil && !isBucketAlreadyOwned(err) {
		return nil, fmt.Errorf("create bucket %s: %w", req.Name, err)
	}

	return &cpi.Bucket{
		ID:           req.Name,
		Name:         req.Name,
		Region:       cli.region,
		StorageClass: "",
		Versioning:   false,
		Encryption:   false,
		Public:       false,
		Size:         0,
		ObjectCount:  0,
		Tags:         req.Tags,
		CreatedAt:    time.Now(),
	}, nil
}

// getBucketExternal returns metadata for a single bucket via S3 HEAD.
func (m *StorageManager) getBucketExternal(ctx context.Context, name string) (*cpi.Bucket, error) {
	cli, err := m.blobstoreClient()
	if err != nil {
		return nil, err
	}

	_, err = cli.cli.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(name)})
	if err != nil {
		return nil, fmt.Errorf("head bucket %s: %w", name, err)
	}

	return &cpi.Bucket{
		ID:           name,
		Name:         name,
		Region:       cli.region,
		StorageClass: "",
		Versioning:   false,
		Encryption:   false,
		Public:       false,
		Size:         0,
		ObjectCount:  0,
		Tags:         map[string]string{},
		CreatedAt:    time.Now(),
	}, nil
}

// listBucketsExternal lists all buckets visible to the configured credentials.
func (m *StorageManager) listBucketsExternal(ctx context.Context) ([]*cpi.Bucket, error) {
	cli, err := m.blobstoreClient()
	if err != nil {
		return nil, err
	}

	resp, err := cli.cli.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, fmt.Errorf("list buckets: %w", err)
	}

	out := make([]*cpi.Bucket, 0, len(resp.Buckets))

	for _, b := range resp.Buckets {
		name := ""
		if b.Name != nil {
			name = *b.Name
		}

		created := time.Now()
		if b.CreationDate != nil {
			created = *b.CreationDate
		}

		out = append(out, &cpi.Bucket{
			ID:           name,
			Name:         name,
			Region:       cli.region,
			StorageClass: "",
			Versioning:   false,
			Encryption:   false,
			Public:       false,
			Size:         0,
			ObjectCount:  0,
			Tags:         map[string]string{},
			CreatedAt:    created,
		})
	}

	return out, nil
}

// deleteBucketExternal removes a bucket. The caller is expected to empty it
// first; S3 rejects DeleteBucket on a non-empty bucket.
func (m *StorageManager) deleteBucketExternal(ctx context.Context, name string) error {
	cli, err := m.blobstoreClient()
	if err != nil {
		return err
	}

	_, err = cli.cli.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(name)})
	if err != nil {
		return fmt.Errorf("delete bucket %s: %w", name, err)
	}

	return nil
}

// emptyBucketExternal paginates through bucket contents and batches deletes.
func (m *StorageManager) emptyBucketExternal(ctx context.Context, name string) error {
	cli, err := m.blobstoreClient()
	if err != nil {
		return err
	}

	var continuationToken *string

	for {
		page, listErr := cli.cli.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(name),
			ContinuationToken: continuationToken,
		})
		if listErr != nil {
			return fmt.Errorf("list objects in %s: %w", name, listErr)
		}

		if len(page.Contents) == 0 {
			if page.IsTruncated == nil || !*page.IsTruncated {
				return nil
			}

			continuationToken = page.NextContinuationToken

			continue
		}

		err = deleteObjectBatch(ctx, cli.cli, name, page.Contents)
		if err != nil {
			return err
		}

		if page.IsTruncated == nil || !*page.IsTruncated {
			return nil
		}

		continuationToken = page.NextContinuationToken
	}
}

func deleteObjectBatch(ctx context.Context, cli *s3.Client, bucket string, objects []s3types.Object) error {
	for start := 0; start < len(objects); start += blobstoreDeleteBatchSize {
		end := start + blobstoreDeleteBatchSize
		if end > len(objects) {
			end = len(objects)
		}

		ids := make([]s3types.ObjectIdentifier, 0, end-start)
		for _, obj := range objects[start:end] {
			ids = append(ids, s3types.ObjectIdentifier{Key: obj.Key})
		}

		_, err := cli.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(bucket),
			Delete: &s3types.Delete{Objects: ids},
		})
		if err != nil {
			return fmt.Errorf("delete object batch in %s: %w", bucket, err)
		}
	}

	return nil
}

// isBucketEmptyExternal returns true when no objects are visible.
func (m *StorageManager) isBucketEmptyExternal(ctx context.Context, name string) (bool, error) {
	cli, err := m.blobstoreClient()
	if err != nil {
		return false, err
	}

	resp, err := cli.cli.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket:  aws.String(name),
		MaxKeys: aws.Int32(1),
	})
	if err != nil {
		return false, fmt.Errorf("list objects in %s: %w", name, err)
	}

	return len(resp.Contents) == 0, nil
}

// SetBucketVersioning satisfies the bootstrap bucketVersioner interface for
// external mode. Local-mode StorageManager calls return ErrBucketsNotSupported
// via the underlying blobstoreClient() guard.
func (m *StorageManager) SetBucketVersioning(ctx context.Context, name string, enabled bool) error {
	cli, err := m.blobstoreClient()
	if err != nil {
		return err
	}

	status := s3types.BucketVersioningStatusEnabled
	if !enabled {
		status = s3types.BucketVersioningStatusSuspended
	}

	_, err = cli.cli.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{
		Bucket: aws.String(name),
		VersioningConfiguration: &s3types.VersioningConfiguration{
			Status: status,
		},
	})
	if err != nil {
		return fmt.Errorf("set bucket versioning %s: %w", name, err)
	}

	return nil
}

// SetBucketLifecycle satisfies the bootstrap bucketLifecycler interface for
// external mode.
func (m *StorageManager) SetBucketLifecycle(ctx context.Context, name string, noncurrentDays int) error {
	cli, err := m.blobstoreClient()
	if err != nil {
		return err
	}

	if noncurrentDays <= 0 {
		return nil
	}

	if noncurrentDays > math.MaxInt32 {
		return fmt.Errorf("set bucket lifecycle %s: noncurrentDays %d exceeds int32 max", name, noncurrentDays)
	}

	days := int32(noncurrentDays) //nolint:gosec // bounded to [1, MaxInt32] above

	_, err = cli.cli.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{
		Bucket: aws.String(name),
		LifecycleConfiguration: &s3types.BucketLifecycleConfiguration{
			Rules: []s3types.LifecycleRule{
				{
					ID:     aws.String(blobstoreLifecycleRuleID),
					Status: s3types.ExpirationStatusEnabled,
					NoncurrentVersionExpiration: &s3types.NoncurrentVersionExpiration{
						NoncurrentDays: aws.Int32(days),
					},
					Filter: &s3types.LifecycleRuleFilter{Prefix: aws.String("")},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("set bucket lifecycle %s: %w", name, err)
	}

	return nil
}

// isBucketAlreadyOwned recognises S3 "you already own this bucket" responses
// so CreateBucket is idempotent across re-runs.
func isBucketAlreadyOwned(err error) bool {
	if err == nil {
		return false
	}

	var (
		owned  *s3types.BucketAlreadyOwnedByYou
		exists *s3types.BucketAlreadyExists
	)

	return errors.As(err, &owned) || errors.As(err, &exists)
}

// Compile-time checks that StorageManager satisfies the bootstrap-side
// versioner/lifecycler interfaces when running in external mode.
var (
	_ versionerIface  = (*StorageManager)(nil)
	_ lifecyclerIface = (*StorageManager)(nil)
)

type versionerIface interface {
	SetBucketVersioning(ctx context.Context, name string, enabled bool) error
}

type lifecyclerIface interface {
	SetBucketLifecycle(ctx context.Context, name string, noncurrentDays int) error
}
