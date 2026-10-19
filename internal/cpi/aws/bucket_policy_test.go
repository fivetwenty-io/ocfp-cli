package aws

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"testing"
)

type policyS3 struct {
	S3API
	version *s3.PutBucketVersioningInput
	policy  *s3.PutBucketLifecycleConfigurationInput
}

func (f *policyS3) PutBucketVersioning(_ context.Context, in *s3.PutBucketVersioningInput, _ ...func(*s3.Options)) (*s3.PutBucketVersioningOutput, error) {
	f.version = in
	return &s3.PutBucketVersioningOutput{}, nil
}
func (f *policyS3) GetBucketLifecycleConfiguration(context.Context, *s3.GetBucketLifecycleConfigurationInput, ...func(*s3.Options)) (*s3.GetBucketLifecycleConfigurationOutput, error) {
	return &s3.GetBucketLifecycleConfigurationOutput{Rules: []types.LifecycleRule{{ID: aws.String("operator-owned"), Status: types.ExpirationStatusEnabled}}}, nil
}
func (f *policyS3) PutBucketLifecycleConfiguration(_ context.Context, in *s3.PutBucketLifecycleConfigurationInput, _ ...func(*s3.Options)) (*s3.PutBucketLifecycleConfigurationOutput, error) {
	f.policy = in
	return &s3.PutBucketLifecycleConfigurationOutput{}, nil
}
func (f *policyS3) DeleteBucketLifecycle(context.Context, *s3.DeleteBucketLifecycleInput, ...func(*s3.Options)) (*s3.DeleteBucketLifecycleOutput, error) {
	return &s3.DeleteBucketLifecycleOutput{}, nil
}
func TestParityAWSBucketPolicyCapabilities(t *testing.T) {
	f := &policyS3{}
	m := &StorageManager{s3: f}
	v, ok := any(m).(interface {
		SetBucketVersioning(context.Context, string, bool) error
	})
	if !ok {
		t.Fatal("AWS silently skips requested versioning")
	}
	if err := v.SetBucketVersioning(context.Background(), "bucket", true); err != nil {
		t.Fatal(err)
	}
	if f.version.VersioningConfiguration.Status != types.BucketVersioningStatusEnabled {
		t.Fatal("versioning not enabled")
	}
	l, ok := any(m).(interface {
		SetBucketLifecycle(context.Context, string, int) error
	})
	if !ok {
		t.Fatal("AWS silently skips requested lifecycle")
	}
	if err := l.SetBucketLifecycle(context.Background(), "bucket", 30); err != nil {
		t.Fatal(err)
	}
	rules := f.policy.LifecycleConfiguration.Rules
	if len(rules) != 2 || aws.ToString(rules[0].ID) != "operator-owned" || aws.ToInt32(rules[1].NoncurrentVersionExpiration.NoncurrentDays) != 30 {
		t.Fatal("lifecycle replaced operator rules or missed retention")
	}
}
