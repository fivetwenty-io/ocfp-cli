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

func TestLegacyNumericInstanceResolvesOriginalZone(t *testing.T) {
	mutations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/operations/") {
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
			return
		}
		if strings.Contains(r.URL.Path, "/aggregated/instances") {
			_, _ = w.Write([]byte(`{"items":{"zones/r-b":{"instances":[{"id":"42","name":"vm","zone":"projects/p/zones/r-b","status":"TERMINATED"}]}}}`))
			return
		}
		if strings.Contains(r.URL.Path, "/zones/r-b/instances/vm") {
			if r.Method == "POST" {
				mutations++
				_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
				return
			}
			_, _ = w.Write([]byte(`{"id":"42","name":"vm","zone":"projects/p/zones/r-b","status":"TERMINATED"}`))
			return
		}
		t.Errorf("numeric identity used wrong scope: %s", r.URL.Path)
		http.Error(w, `{"error":{"code":404,"message":"not found"}}`, 404)
	}))
	defer server.Close()
	api, err := compute.NewInstancesRESTClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	m := &ComputeManager{client: &Client{clientsLoaded: true, instancesClient: api, config: &Config{ProjectID: "p", Zone: "r-a"}}}
	vm, err := m.GetInstance(context.Background(), "42")
	if err != nil || vm.ID != "projects/p/zones/r-b/instances/vm" {
		t.Fatalf("get: %+v %v", vm, err)
	}
	if err := m.StartInstance(context.Background(), "42"); err != nil {
		t.Fatal(err)
	}
	if mutations != 1 {
		t.Fatalf("start mutations=%d", mutations)
	}
}

func TestLegacyNumericDiskResolvesOriginalZone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/aggregated/disks") {
			_, _ = w.Write([]byte(`{"items":{"zones/r-b":{"disks":[{"id":"43","name":"data","zone":"projects/p/zones/r-b","status":"READY"}]}}}`))
			return
		}
		t.Errorf("numeric disk used wrong scope: %s", r.URL.Path)
		http.Error(w, `{"error":{"code":404}}`, 404)
	}))
	defer server.Close()
	api, err := compute.NewDisksRESTClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	m := &StorageManager{client: &Client{clientsLoaded: true, disksClient: api, config: &Config{ProjectID: "p", Zone: "r-a"}}}
	disk, err := m.GetVolume(context.Background(), "43")
	if err != nil || disk.ID != "projects/p/zones/r-b/disks/data" {
		t.Fatalf("get: %+v %v", disk, err)
	}
}

func TestLegacyMissingDeletionIsIdempotent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || !strings.Contains(r.URL.Path, "/aggregated/") {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":{}}`))
	}))
	defer server.Close()
	opts := []option.ClientOption{option.WithEndpoint(server.URL), option.WithoutAuthentication()}
	instances, err := compute.NewInstancesRESTClient(context.Background(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	defer instances.Close()
	disks, err := compute.NewDisksRESTClient(context.Background(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	defer disks.Close()
	client := &Client{clientsLoaded: true, instancesClient: instances, disksClient: disks, config: &Config{ProjectID: "p", Zone: "r-a"}}
	if err := (&ComputeManager{client: client}).DeleteInstance(context.Background(), "42"); err != nil {
		t.Error(err)
	}
	if err := (&StorageManager{client: client}).DeleteVolume(context.Background(), "43"); err != nil {
		t.Error(err)
	}
}
