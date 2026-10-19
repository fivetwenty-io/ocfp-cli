package gcp

import (
	compute "cloud.google.com/go/compute/apiv1"
	"context"
	"encoding/json"
	"google.golang.org/api/option"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParityDiskDetachUsesAttachedDevice(t *testing.T) {
	detached := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/operations/") {
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/detachDisk") {
			if !strings.Contains(r.URL.Path, "/zones/r-b/instances/vm/") || r.URL.Query().Get("deviceName") != "ocfp-bastion-data" {
				t.Errorf("wrong detach: %s", r.URL.String())
			}
			detached = true
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
			return
		}
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/instances/vm") {
			_, _ = w.Write([]byte(`{"name":"vm","disks":[{"source":"projects/p/zones/r-b/disks/data","deviceName":"ocfp-bastion-data","boot":false}]}`))
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		http.Error(w, `{"error":{"code":400}}`, 400)
	}))
	defer server.Close()
	api, err := compute.NewInstancesRESTClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	m := &StorageManager{client: &Client{config: &Config{ProjectID: "p", Zone: "r-a"}, clientsLoaded: true, instancesClient: api}}
	if err = m.DetachVolume(context.Background(), "projects/p/zones/r-b/disks/data", "projects/p/zones/r-b/instances/vm"); err != nil {
		t.Fatal(err)
	}
	if !detached {
		t.Fatal("disk not detached")
	}
}
func TestParityDiskAttachIdempotent(t *testing.T) {
	calls := 0
	foreign := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "GET" {
			t.Errorf("existing attachment mutated: %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"name": "vm", "disks": []interface{}{map[string]interface{}{"source": func() string {
			if foreign {
				return "projects/foreign/zones/r-b/disks/data"
			}
			return "projects/p/zones/r-b/disks/data"
		}(), "deviceName": "ocfp-bastion-data", "boot": false}}})
	}))
	defer server.Close()
	api, err := compute.NewInstancesRESTClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	m := &StorageManager{client: &Client{config: &Config{ProjectID: "p", Zone: "r-a"}, clientsLoaded: true, instancesClient: api}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err = m.AttachVolume(ctx, "projects/p/zones/r-b/disks/data", "projects/p/zones/r-b/instances/vm", "ocfp-bastion-data"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("got %d calls", calls)
	}
	foreign = true
	if err = m.AttachVolume(ctx, "projects/p/zones/r-b/disks/data", "projects/p/zones/r-b/instances/vm", "ocfp-bastion-data"); err == nil {
		t.Error("adopted foreign project disk with same name")
	}
}

func TestParityLegacyDiskIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/aggregated/disks"):
			_, _ = w.Write([]byte(`{"items":{"zones/r-b":{"disks":[{"name":"data","id":"42","zone":"projects/p/zones/r-b"}]}}}`))
		case strings.HasSuffix(r.URL.Path, "/disks/data"):
			_, _ = w.Write([]byte(`{"name":"data","id":"42","zone":"projects/p/zones/r-b"}`))
		case strings.HasSuffix(r.URL.Path, "/instances/vm"):
			_, _ = w.Write([]byte(`{"name":"vm","disks":[{"source":"projects/p/zones/r-b/disks/data","deviceName":"ocfp-bastion-data"}]}`))
		default:
			t.Errorf("unexpected mutation/query %s %s", r.Method, r.URL)
			http.Error(w, `{"error":{"code":400}}`, 400)
		}
	}))
	defer server.Close()
	instances, err := compute.NewInstancesRESTClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer instances.Close()
	disks, err := compute.NewDisksRESTClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer disks.Close()
	m := &StorageManager{client: &Client{clientsLoaded: true, instancesClient: instances, disksClient: disks, config: &Config{ProjectID: "p", Zone: "r-b"}}}
	if err := m.AttachVolume(context.Background(), "42", "projects/p/zones/r-b/instances/vm", "ocfp-bastion-data"); err != nil {
		t.Fatal(err)
	}
}
