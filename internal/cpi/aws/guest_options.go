package aws

import (
	"context"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

var ErrUnsupportedFeature = errors.New("AWS feature is not supported")

type instanceAttributeAPI interface {
	ModifyInstanceAttribute(ctx context.Context, input *ec2.ModifyInstanceAttributeInput, opts ...func(*ec2.Options)) (*ec2.ModifyInstanceAttributeOutput, error)
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}

	return aws.String(value)
}

// EnsureGuestOptions changes protection only at the caller's explicit request.
// EC2 owns host recovery; StartOnBoot has no separately configurable equivalent.
func (m *ComputeManager) EnsureGuestOptions(ctx context.Context, id string, options cpi.GuestOptions) error {
	client, err := m.getEC2(ctx)
	if err != nil {
		return err
	}

	modifier, ok := client.(instanceAttributeAPI)
	if !ok {
		return fmt.Errorf("AWS guest attribute API unavailable: %w", ErrUnsupportedFeature)
	}

	_, err = modifier.ModifyInstanceAttribute(ctx, &ec2.ModifyInstanceAttributeInput{InstanceId: aws.String(id), DisableApiTermination: &types.AttributeBooleanValue{Value: aws.Bool(options.Protected)}})
	if err != nil {
		return fmt.Errorf("set instance termination protection: %w", err)
	}

	return nil
}
