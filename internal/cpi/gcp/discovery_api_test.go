package gcp

import (
	compute "cloud.google.com/go/compute/apiv1"
	"context"
	"google.golang.org/api/option"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParityAllZoneDiscovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(r.URL.Path, "/aggregated/instances") {
			t.Errorf("zonal discovery misses resources: %s", r.URL)
			_, _ = w.Write([]byte(`{"items":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":{"zones/r-a":{"instances":[{"name":"a","zone":"projects/p/zones/r-a","labels":{"ocfp-bloc":"bloc"}}]},"zones/r-b":{"instances":[{"name":"b","zone":"projects/p/zones/r-b","labels":{"ocfp-bloc":"bloc"}}]}}}`))
	}))
	defer server.Close()
	api, err := compute.NewInstancesRESTClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	m := &ComputeManager{client: &Client{config: &Config{ProjectID: "p", Zone: "r-a"}, instancesClient: api, clientsLoaded: true}}
	instances, err := m.ListInstances(context.Background(), map[string]string{"ocfp:bloc": "bloc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 2 {
		t.Fatalf("found %d instances", len(instances))
	}
}

func TestParityAllZoneDiskDiscovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(r.URL.Path, "/aggregated/disks") {
			t.Errorf("zonal discovery: %s", r.URL)
			_, _ = w.Write([]byte(`{"items":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":{"zones/r-b":{"disks":[{"name":"data","zone":"projects/p/zones/r-b"}]}}}`))
	}))
	defer server.Close()
	api, err := compute.NewDisksRESTClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	m := &StorageManager{client: &Client{config: &Config{ProjectID: "p", Zone: "r-a"}, disksClient: api, clientsLoaded: true}}
	disks, err := m.ListVolumes(context.Background(), nil)
	if err != nil || len(disks) != 1 {
		t.Fatalf("disks=%v err=%v", disks, err)
	}
}
