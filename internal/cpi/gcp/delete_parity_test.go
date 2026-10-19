package gcp

import (
	compute "cloud.google.com/go/compute/apiv1"
	"context"
	"google.golang.org/api/option"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParityDeleteAbsentResources(t *testing.T) {
	status := 404
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, http.StatusText(status), status) }))
	defer server.Close()
	ctx := context.Background()
	opts := []option.ClientOption{option.WithEndpoint(server.URL), option.WithoutAuthentication()}
	networks, _ := compute.NewNetworksRESTClient(ctx, opts...)
	defer networks.Close()
	subnets, _ := compute.NewSubnetworksRESTClient(ctx, opts...)
	defer subnets.Close()
	addresses, _ := compute.NewAddressesRESTClient(ctx, opts...)
	defer addresses.Close()
	routers, _ := compute.NewRoutersRESTClient(ctx, opts...)
	defer routers.Close()
	disks, _ := compute.NewDisksRESTClient(ctx, opts...)
	defer disks.Close()
	snapshots, _ := compute.NewSnapshotsRESTClient(ctx, opts...)
	defer snapshots.Close()
	client := &Client{clientsLoaded: true, config: &Config{ProjectID: "p", Zone: "r-a", Region: "r"}, networksClient: networks, subnetworksClient: subnets, addressesClient: addresses, routersClient: routers, disksClient: disks, snapshotsClient: snapshots}
	network := &NetworkManager{client: client}
	storage := &StorageManager{client: client}
	for name, remove := range map[string]func(context.Context, string) error{"network": network.DeleteNetwork, "subnet": network.DeleteSubnet, "address": network.DeletePublicIP, "router": network.DeleteRouter, "disk": storage.DeleteVolume, "snapshot": storage.DeleteSnapshot} {
		t.Run(name, func(t *testing.T) {
			status = 404
			if err := remove(ctx, "absent"); err != nil {
				t.Errorf("absent delete: %v", err)
			}
			status = 403
			if err := remove(ctx, "forbidden"); err == nil {
				t.Error("ignored permission failure")
			}
		})
	}
}
