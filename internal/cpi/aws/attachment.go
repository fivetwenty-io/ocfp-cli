package aws

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"time"
)

func (m *StorageManager) waitForAttachment(ctx context.Context, client EC2API, volumeID, instanceID, device string) error {
	ctx, cancel := context.WithTimeout(ctx, volumeAttachTimeout)
	defer cancel()

	interval := m.pollInterval
	if interval <= 0 {
		interval = volumePollInterval
	}

	for {
		out, err := client.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{VolumeIds: []string{volumeID}})
		if err != nil {
			return fmt.Errorf("inspect attaching volume: %w", err)
		}

		if len(out.Volumes) != 1 {
			return fmt.Errorf("volume %s not found: %w", volumeID, ErrNotFound)
		}

		for _, a := range out.Volumes[0].Attachments {
			if aws.ToString(a.InstanceId) != instanceID || aws.ToString(a.Device) != device {
				return fmt.Errorf("volume %s attached to an unexpected instance/device: %w", volumeID, ErrInvalidRequest)
			}

			if a.State == types.VolumeAttachmentStateAttached {
				return nil
			}
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()

			return fmt.Errorf("wait for attachment %s: %w", volumeID, ctx.Err())
		case <-timer.C:
		}
	}
}
