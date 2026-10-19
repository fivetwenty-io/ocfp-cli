package gcp

import (
	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"context"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/encoding/protojson"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParityNetworkConflictAdoption(t *testing.T) {
	owned := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			http.Error(w, `{"error":{"code":409,"message":"exists"}}`, 409)
			return
		}
		description := "foreign"
		if owned {
			description = resourceDescription("", map[string]string{"ocfp:bloc": "bloc"})
		}
		b, _ := protojson.Marshal(&computepb.Network{Name: proto("net"), Description: proto(description), AutoCreateSubnetworks: proto(false)})
		_, _ = w.Write(b)
	}))
	defer server.Close()
	api, err := compute.NewNetworksRESTClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	m := &NetworkManager{client: &Client{clientsLoaded: true, networksClient: api, config: &Config{ProjectID: "p"}}}
	req := &cpi.NetworkRequest{Name: "net", Tags: map[string]string{"ocfp:bloc": "bloc"}}
	network, err := m.CreateNetwork(context.Background(), req)
	if err != nil || network == nil {
		t.Fatalf("matching retry: %v", err)
	}
	owned = false
	if _, err = m.CreateNetwork(context.Background(), req); err == nil {
		t.Fatal("adopted foreign network")
	}
}

func TestParityInstanceConflictAdoption(t *testing.T) {
	var instance *computepb.Instance
	drift := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			http.Error(w, `{"error":{"code":409,"message":"exists"}}`, 409)
			return
		}
		if strings.Contains(r.URL.Path, "/instances/vm") {
			if drift {
				instance.MachineType = proto("zones/r-a/machineTypes/other")
			}
			b, _ := protojson.Marshal(instance)
			_, _ = w.Write(b)
			return
		}
		t.Errorf("unexpected request: %s", r.URL)
	}))
	defer server.Close()
	api, err := compute.NewInstancesRESTClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	m := &ComputeManager{client: &Client{clientsLoaded: true, instancesClient: api, config: &Config{ProjectID: "p", Zone: "r-a"}}}
	req := &cpi.InstanceRequest{Name: "vm", Flavor: "e2-small", Tags: map[string]string{"ocfp:bloc": "bloc"}}
	instance = &computepb.Instance{Name: proto("vm"), Zone: proto("projects/p/zones/r-a"), MachineType: proto("zones/r-a/machineTypes/e2-small"), Labels: BuildLabels("vm", req.Tags), Status: proto("RUNNING")}
	vm, err := m.CreateInstance(context.Background(), req)
	if err != nil || vm == nil {
		t.Fatalf("matching retry: %v", err)
	}
	drift = true
	if _, err = m.CreateInstance(context.Background(), req); err == nil {
		t.Fatal("adopted drifted VM")
	}
}

func TestParityDiskConflictAdoption(t *testing.T) {
	drift := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			http.Error(w, `{"error":{"code":409,"message":"exists"}}`, 409)
			return
		}
		size := int64(10)
		if drift {
			size = 5
		}
		b, _ := protojson.Marshal(&computepb.Disk{Name: proto("data"), SizeGb: proto(size), Type: proto("projects/p/zones/r-a/diskTypes/pd-balanced"), Zone: proto("projects/p/zones/r-a"), Labels: BuildLabels("data", map[string]string{"ocfp:bloc": "bloc"})})
		_, _ = w.Write(b)
	}))
	defer server.Close()
	api, err := compute.NewDisksRESTClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	m := &StorageManager{client: &Client{clientsLoaded: true, disksClient: api, config: &Config{ProjectID: "p", Zone: "r-a"}}}
	req := &cpi.VolumeRequest{Name: "data", Size: 10, Tags: map[string]string{"ocfp:bloc": "bloc"}}
	volume, err := m.CreateVolume(context.Background(), req)
	if err != nil || volume == nil {
		t.Fatalf("matching retry: %v", err)
	}
	drift = true
	if _, err = m.CreateVolume(context.Background(), req); err == nil {
		t.Fatal("adopted undersized disk")
	}
}

func TestParitySubnetConflictAdoption(t *testing.T) {
	drift := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			http.Error(w, `{"error":{"code":409,"message":"exists"}}`, 409)
			return
		}
		cidr := "10.0.0.0/24"
		if drift {
			cidr = "10.1.0.0/24"
		}
		b, _ := protojson.Marshal(&computepb.Subnetwork{Name: proto("sub"), IpCidrRange: proto(cidr), Network: proto("projects/p/global/networks/net"), Description: proto(resourceDescription("", map[string]string{"ocfp:bloc": "bloc"}))})
		_, _ = w.Write(b)
	}))
	defer server.Close()
	api, err := compute.NewSubnetworksRESTClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	m := &NetworkManager{client: &Client{clientsLoaded: true, subnetworksClient: api, config: &Config{ProjectID: "p", Region: "r"}}}
	req := &cpi.SubnetRequest{Name: "sub", NetworkID: "net", CIDR: "10.0.0.0/24", Tags: map[string]string{"ocfp:bloc": "bloc"}}
	subnet, err := m.CreateSubnet(context.Background(), req)
	if err != nil || subnet == nil {
		t.Fatalf("matching retry: %v", err)
	}
	drift = true
	if _, err = m.CreateSubnet(context.Background(), req); err == nil {
		t.Fatal("adopted wrong CIDR")
	}
}
