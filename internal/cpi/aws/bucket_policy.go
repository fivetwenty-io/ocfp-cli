package aws

import (
	"context"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"math"
)

type bucketPolicyAPI interface {
	PutBucketVersioning(ctx context.Context, input *s3.PutBucketVersioningInput, opts ...func(*s3.Options)) (*s3.PutBucketVersioningOutput, error)
	GetBucketLifecycleConfiguration(ctx context.Context, input *s3.GetBucketLifecycleConfigurationInput, opts ...func(*s3.Options)) (*s3.GetBucketLifecycleConfigurationOutput, error)
	PutBucketLifecycleConfiguration(ctx context.Context, input *s3.PutBucketLifecycleConfigurationInput, opts ...func(*s3.Options)) (*s3.PutBucketLifecycleConfigurationOutput, error)
	DeleteBucketLifecycle(ctx context.Context, input *s3.DeleteBucketLifecycleInput, opts ...func(*s3.Options)) (*s3.DeleteBucketLifecycleOutput, error)
}

const managedNoncurrentRule = "ocfp-noncurrent-expiration"

func (m *StorageManager) bucketPolicyClient(ctx context.Context) (bucketPolicyAPI, error) {
	client, err := m.getS3(ctx)
	if err != nil {
		return nil, err
	}

	policy, ok := client.(bucketPolicyAPI)
	if !ok {
		return nil, fmt.Errorf("S3 policy API unavailable: %w", ErrUnsupportedFeature)
	}

	return policy, nil
}

// SetBucketVersioning implements bootstrap's versioning capability.
func (m *StorageManager) SetBucketVersioning(ctx context.Context, name string, enabled bool) error {
	client, err := m.bucketPolicyClient(ctx)
	if err != nil {
		return err
	}

	status := types.BucketVersioningStatusSuspended
	if enabled {
		status = types.BucketVersioningStatusEnabled
	}

	_, err = client.PutBucketVersioning(ctx, &s3.PutBucketVersioningInput{Bucket: aws.String(name), VersioningConfiguration: &types.VersioningConfiguration{Status: status}})
	if err != nil {
		return fmt.Errorf("set S3 bucket versioning: %w", err)
	}

	return nil
}

// SetBucketLifecycle changes only ocfp's named noncurrent-version rule.
// S3 replaces the whole configuration on PUT, so unrelated rules must survive.
func (m *StorageManager) SetBucketLifecycle(ctx context.Context, name string, days int) error {
	if days < 0 || days > math.MaxInt32 {
		return fmt.Errorf("invalid retention days %d: %w", days, ErrInvalidRequest)
	}

	client, err := m.bucketPolicyClient(ctx)
	if err != nil {
		return err
	}

	existing, err := client.GetBucketLifecycleConfiguration(ctx, &s3.GetBucketLifecycleConfigurationInput{Bucket: aws.String(name)})
	if err != nil {
		var apiErr smithy.APIError
		if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "NoSuchLifecycleConfiguration" {
			return fmt.Errorf("read S3 lifecycle before updating: %w", err)
		}

		existing = &s3.GetBucketLifecycleConfigurationOutput{}
	}

	rules := make([]types.LifecycleRule, 0, len(existing.Rules)+1)
	for _, rule := range existing.Rules {
		if aws.ToString(rule.ID) != managedNoncurrentRule {
			rules = append(rules, rule)
		}
	}

	if days > 0 {
		rules = append(rules, types.LifecycleRule{ID: aws.String(managedNoncurrentRule), Status: types.ExpirationStatusEnabled, Filter: &types.LifecycleRuleFilter{Prefix: aws.String("")}, NoncurrentVersionExpiration: &types.NoncurrentVersionExpiration{NoncurrentDays: aws.Int32(int32(days))}})
	}

	if len(rules) == 0 {
		if len(existing.Rules) == 0 {
			return nil
		}

		_, err = client.DeleteBucketLifecycle(ctx, &s3.DeleteBucketLifecycleInput{Bucket: aws.String(name)})
	} else {
		_, err = client.PutBucketLifecycleConfiguration(ctx, &s3.PutBucketLifecycleConfigurationInput{Bucket: aws.String(name), LifecycleConfiguration: &types.BucketLifecycleConfiguration{Rules: rules}, TransitionDefaultMinimumObjectSize: existing.TransitionDefaultMinimumObjectSize})
	}

	if err != nil {
		return fmt.Errorf("update S3 lifecycle: %w", err)
	}

	return nil
}
