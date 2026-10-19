package gcp

import (
	"cloud.google.com/go/compute/apiv1/computepb"
	"context"
	"errors"
	"fmt"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"google.golang.org/api/iterator"
	"strconv"
)

func (m *ComputeManager) legacyInstance(ctx context.Context, id string) (*computepb.Instance, error) {
	it := m.client.getInstancesClient().AggregatedList(ctx, &computepb.AggregatedListInstancesRequest{Project: m.client.getConfig().ProjectID})

	var found *computepb.Instance

	for {
		pair, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "resolve numeric instance")
		}

		for _, vm := range pair.Value.GetInstances() {
			if strconv.FormatUint(vm.GetId(), 10) == id {
				if found != nil {
					return nil, fmt.Errorf("ambiguous numeric instance %s", id)
				}

				found = vm
			}
		}
	}

	if found == nil {
		return nil, &cpi.ProviderError{Provider: "gcp", Code: "NotFound", Message: "numeric instance not found"}
	}

	return found, nil
}
func (m *ComputeManager) canonicalInstanceID(ctx context.Context, id string) (string, error) {
	if !isNumericResource(id) {
		return id, nil
	}

	vm, err := m.legacyInstance(ctx, id)
	if err != nil {
		return "", err
	}

	return resourceIdentity(m.client.getConfig(), vm.GetZone(), "instances", vm.GetName()), nil
}
func (m *StorageManager) legacyDisk(ctx context.Context, id string) (*computepb.Disk, error) {
	it := m.client.getDisksClient().AggregatedList(ctx, &computepb.AggregatedListDisksRequest{Project: m.client.getConfig().ProjectID})

	var found *computepb.Disk

	for {
		pair, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}

		if err != nil {
			return nil, WrapGCPError(err, "resolve numeric disk")
		}

		for _, disk := range pair.Value.GetDisks() {
			if strconv.FormatUint(disk.GetId(), 10) == id {
				if found != nil {
					return nil, fmt.Errorf("ambiguous numeric disk %s", id)
				}

				found = disk
			}
		}
	}

	if found == nil {
		return nil, &cpi.ProviderError{Provider: "gcp", Code: "NotFound", Message: "numeric disk not found"}
	}

	return found, nil
}
func (m *StorageManager) canonicalDiskID(ctx context.Context, id string) (string, error) {
	if !isNumericResource(id) {
		return id, nil
	}

	disk, err := m.legacyDisk(ctx, id)
	if err != nil {
		return "", err
	}

	return resourceIdentity(m.client.getConfig(), disk.GetZone(), "disks", disk.GetName()), nil
}
