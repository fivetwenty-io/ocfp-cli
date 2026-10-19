package gcp

import (
	compute "cloud.google.com/go/compute/apiv1"
	"context"
	"encoding/json"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"google.golang.org/api/option"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParityInstanceCreateDelete(t *testing.T) {
	var created map[string]interface{}
	unprotected := false
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/operations/") {
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/firewalls/42") {
			_, _ = w.Write([]byte(`{"name":"bastion","id":"42"}`))
			return
		}
		if !strings.Contains(r.URL.Path, "/zones/r-b/instances") {
			t.Errorf("wrong zone: %s", r.URL)
		}
		if strings.HasSuffix(r.URL.Path, "/setDeletionProtection") {
			unprotected = r.URL.Query().Get("deletionProtection") == "false"
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
			return
		}
		switch r.Method {
		case "POST":
			_ = json.NewDecoder(r.Body).Decode(&created)
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
		case "GET":
			_, _ = w.Write([]byte(`{"name":"vm","zone":"projects/p/zones/r-b","status":"RUNNING","deletionProtection":true}`))
		case "DELETE":
			if !unprotected {
				t.Error("deleted without clearing protection")
			}
			deleted = true
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
		}
	}))
	defer server.Close()
	api, err := compute.NewInstancesRESTClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	firewalls, err := compute.NewFirewallsRESTClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer firewalls.Close()
	m := &ComputeManager{client: &Client{clientsLoaded: true, instancesClient: api, firewallsClient: firewalls, config: &Config{ProjectID: "p", Region: "r", Zone: "r-a"}}}
	public := false
	vm, err := m.CreateInstance(context.Background(), &cpi.InstanceRequest{Name: "vm", SecurityGroupIDs: []string{"42"}, AvailabilityZone: "r-b", Flavor: "e2-small", Image: "projects/ubuntu-os-cloud/global/images/family/ubuntu-2204-lts", SubnetID: "sub", StaticPrivateIP: "10.1.2.3", PublicIP: &public, Protected: true, IPForwarding: true, UserData: "#cloud-config\nhostname: vm", PublicKey: "ssh-ed25519 AAAA", DefaultUsername: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if created["deletionProtection"] != true || created["canIpForward"] != true {
		t.Fatalf("missing guest flags: %v", created)
	}
	tags := created["tags"].(map[string]interface{})["items"].([]interface{})
	if len(tags) != 1 || tags[0] != "sg-bastion" {
		t.Errorf("legacy numeric firewall identity not resolved: %v", tags)
	}
	if vm.ID != "projects/p/zones/r-b/instances/vm" {
		t.Fatalf("wrong returned scope: %s", vm.ID)
	}
	if err = m.DeleteInstance(context.Background(), vm.ID); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("not deleted")
	}
}

func TestParityCreatedHandleOnWaitFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"op","status":"DONE","error":{"errors":[{"code":"RESOURCE_ERROR","message":"failed"}]}}`))
	}))
	defer server.Close()
	api, err := compute.NewInstancesRESTClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	m := &ComputeManager{client: &Client{clientsLoaded: true, instancesClient: api, config: &Config{ProjectID: "p", Zone: "r-a"}}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	vm, err := m.CreateInstance(ctx, &cpi.InstanceRequest{Name: "vm", AvailabilityZone: "r-b"})
	if err == nil || vm == nil || vm.ID != "projects/p/zones/r-b/instances/vm" {
		t.Fatalf("lost recovery handle: vm=%v err=%v", vm, err)
	}
}
