package gcp

import (
	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"context"
	"encoding/json"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/encoding/protojson"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParitySecurityLifecycle(t *testing.T) {
	firewalls := map[string]*computepb.Firewall{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Log(r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/operations/") {
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
			return
		}
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		switch r.Method {
		case "POST":
			var raw json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&raw)
			f := &computepb.Firewall{}
			if err := protojson.Unmarshal(raw, f); err != nil {
				t.Error(err)
			}
			firewalls[f.GetName()] = f
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
		case "DELETE":
			delete(firewalls, name)
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
		case "GET":
			if name == "firewalls" {
				list := &computepb.FirewallList{}
				for _, f := range firewalls {
					list.Items = append(list.Items, f)
				}
				b, _ := protojson.Marshal(list)
				_, _ = w.Write(b)
			} else if f := firewalls[name]; f != nil {
				b, _ := protojson.Marshal(f)
				_, _ = w.Write(b)
			} else {
				http.Error(w, `{"error":{"code":404,"message":"absent"}}`, 404)
			}
		}
	}))
	defer server.Close()
	api, err := compute.NewFirewallsRESTClient(context.Background(), option.WithEndpoint(server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	client := &Client{config: &Config{ProjectID: "project"}, clientsLoaded: true, firewallsClient: api}
	manager := &SecurityManager{client: client}
	rules := []*cpi.SecurityRule{{Direction: "ingress", Protocol: "tcp", PortRangeMin: 22, PortRangeMax: 22, RemoteIPCIDR: "10.0.0.0/8"}, {Direction: "ingress", Protocol: "tcp", PortRangeMin: 443, PortRangeMax: 443, RemoteIPCIDR: "192.168.0.0/16"}, {Direction: "egress", Protocol: "udp", PortRangeMin: 53, PortRangeMax: 53, RemoteIPCIDR: "8.8.8.8/32"}}
	group, err := manager.CreateSecurityGroup(context.Background(), &cpi.CreateSecurityGroupRequest{Name: "bastion", NetworkID: "network", Rules: rules, Tags: map[string]string{"ocfp:bloc": "bloc"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(firewalls) != 4 {
		t.Fatalf("want anchor and 3 independent rules, got %d", len(firewalls))
	}
	for _, f := range firewalls {
		if f.GetDisabled() {
			continue
		}
		if len(f.GetAllowed()) != 1 {
			t.Fatal("cross-product permissions")
		}
		if f.GetDirection() == "EGRESS" && (len(f.GetDestinationRanges()) != 1 || len(f.GetSourceRanges()) != 0) {
			t.Fatal("invalid egress scope")
		}
	}
	got, err := manager.ListSecurityRules(context.Background(), group.ID)
	if err != nil || len(got) != 3 {
		t.Fatalf("rules=%v err=%v", got, err)
	}
	if err := manager.AddSecurityRule(context.Background(), group.ID, rules[0]); err != nil {
		t.Fatal(err)
	}
	if len(firewalls) != 4 {
		t.Fatal("rerun duplicated rule")
	}
	if _, err := manager.CreateSecurityGroup(context.Background(), &cpi.CreateSecurityGroupRequest{Name: "bastion", NetworkID: "network", Tags: map[string]string{"ocfp:bloc": "foreign"}}); err == nil {
		t.Error("adopted another bloc's group")
	}
	// Reconciliation must reject drift instead of silently accepting broadened access.
	drifted, _ := makeFirewall(group.ID, "network", rules[0])
	firewalls[drifted.GetName()].SourceRanges = []string{"0.0.0.0/0"}
	if err := manager.AddSecurityRule(context.Background(), group.ID, rules[0]); err == nil {
		t.Error("accepted drifted firewall permissions")
	}
	firewalls[drifted.GetName()].SourceRanges = []string{"10.0.0.0/8"}
	// Foreign empty anchors and disabled children need explicit ownership handling.
	firewalls["foreign"] = &computepb.Firewall{Name: proto("foreign"), Disabled: proto(true)}
	if err := manager.DeleteSecurityGroup(context.Background(), "foreign"); err == nil {
		t.Error("deleted unmanaged firewall")
	}
	delete(firewalls, "foreign")
	disabled, _ := makeFirewall(group.ID, "network", &cpi.SecurityRule{Direction: "ingress", Protocol: "icmp"})
	disabled.Disabled = proto(true)
	firewalls[disabled.GetName()] = disabled
	if err := manager.RemoveSecurityRule(context.Background(), group.ID, got[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.DeleteSecurityGroup(context.Background(), group.ID); err != nil {
		t.Fatal(err)
	}
	if len(firewalls) != 0 {
		t.Fatalf("orphaned rules: %v", firewalls)
	}
}
