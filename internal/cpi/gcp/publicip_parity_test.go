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
)

func TestParityFloatingIPLifecycle(t *testing.T) {
	attached := false
	removed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/operations/") {
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
			return
		}
		if strings.Contains(r.URL.Path, "/addresses/") {
			_, _ = w.Write([]byte(`{"name":"ip","address":"203.0.113.10","users":["projects/p/zones/r-b/instances/vm"]}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/addAccessConfig") {
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["natIP"] != "203.0.113.10" || r.URL.Query().Get("networkInterface") != "nic0" {
				t.Errorf("bad attach %v %s", body, r.URL)
			}
			attached = true
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/deleteAccessConfig") {
			if r.URL.Query().Get("accessConfig") != "External NAT" {
				t.Errorf("bad detach %s", r.URL)
			}
			removed = true
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
			return
		}
		if strings.Contains(r.URL.Path, "/instances/vm") {
			if attached {
				_, _ = w.Write([]byte(`{"name":"vm","networkInterfaces":[{"name":"nic0","accessConfigs":[{"name":"External NAT","natIP":"203.0.113.10"}]}]}`))
			} else {
				_, _ = w.Write([]byte(`{"name":"vm","networkInterfaces":[{"name":"nic0"}]}`))
			}
			return
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL)
	}))
	defer server.Close()
	opts := []option.ClientOption{option.WithEndpoint(server.URL), option.WithoutAuthentication()}
	instances, err := compute.NewInstancesRESTClient(context.Background(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	defer instances.Close()
	addresses, err := compute.NewAddressesRESTClient(context.Background(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	defer addresses.Close()
	m := &NetworkManager{client: &Client{config: &Config{ProjectID: "p", Region: "r", Zone: "r-a"}, clientsLoaded: true, instancesClient: instances, addressesClient: addresses}}
	if err = m.AssociateFloatingIP(context.Background(), "ip", "projects/p/zones/r-b/instances/vm"); err != nil {
		t.Fatal(err)
	}
	if err = m.DisassociateFloatingIP(context.Background(), "ip"); err != nil {
		t.Fatal(err)
	}
	if !attached || !removed {
		t.Fatal("floating IP lifecycle incomplete")
	}
}

func TestParityRegionalAddressIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/projects/other/regions/elsewhere/addresses/ip") {
			t.Errorf("lost scope: %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"name":"ip","region":"projects/other/regions/elsewhere","address":"203.0.113.10"}`))
	}))
	defer server.Close()
	api, err := compute.NewAddressesRESTClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	m := &NetworkManager{client: &Client{clientsLoaded: true, addressesClient: api, config: &Config{ProjectID: "p", Region: "r"}}}
	address, err := m.GetPublicIP(context.Background(), "projects/other/regions/elsewhere/addresses/ip")
	if err != nil || address.ID != "projects/other/regions/elsewhere/addresses/ip" {
		t.Fatalf("address=%v err=%v", address, err)
	}
}
