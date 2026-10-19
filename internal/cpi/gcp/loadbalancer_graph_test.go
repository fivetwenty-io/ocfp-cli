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

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

func TestNativeLBGraphs(t *testing.T) {
	for _, protocol := range []string{"http", "tcp"} {
		t.Run(protocol, func(t *testing.T) {
			fake := newLBTestAPI(t)
			manager := &LoadBalancerManager{client: fake.client}
			lb, err := manager.CreateLoadBalancer(context.Background(), &cpi.CreateLoadBalancerRequest{Name: "service", Protocol: protocol, Port: 2222, TargetPort: 8080, NetworkID: "network", Tags: map[string]string{"ocfp:bloc": "bloc"}})
			if err != nil {
				t.Fatal(err)
			}
			if lb.Port != 2222 || lb.TargetPort != 8080 || lb.Protocol != protocol || lb.IPAddress != "203.0.113.1" {
				t.Fatalf("lost listener: %+v", lb)
			}
			forwarding := fake.objects["global/forwardingRules/service"]
			target := forwarding["target"].(string)
			if protocol == "http" && !strings.Contains(target, "/targetHttpProxies/") {
				t.Errorf("wrong HTTP target %s", target)
			}
			if protocol == "tcp" && !strings.Contains(target, "/targetTcpProxies/") {
				t.Errorf("wrong TCP target %s", target)
			}
			before := fake.posts
			if _, err = manager.CreateLoadBalancer(context.Background(), &cpi.CreateLoadBalancerRequest{Name: "service", Protocol: protocol, Port: 2222, TargetPort: 8080, NetworkID: "network", Tags: map[string]string{"ocfp:bloc": "bloc"}}); err != nil {
				t.Fatal(err)
			}
			if fake.posts != before {
				t.Fatal("retry duplicated graph")
			}
			if err = manager.DeleteLoadBalancer(context.Background(), lb.ID); err != nil {
				t.Fatal(err)
			}
			if len(fake.objects) != 0 {
				t.Fatalf("leaked graph %v", fake.objects)
			}
			if err = manager.DeleteLoadBalancer(context.Background(), lb.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeLBInvalidRequestsHaveNoSideEffects(t *testing.T) {
	for _, req := range []*cpi.CreateLoadBalancerRequest{{Name: "lb", Protocol: "https", Port: 443}, {Name: "lb", Protocol: "tcp", Port: 22, Scheme: "internal"}, {Name: "lb", Protocol: "udp", Port: 53}, {Name: "lb", Protocol: "tcp", Port: 70000}} {
		if _, err := (&LoadBalancerManager{}).CreateLoadBalancer(context.Background(), req); err == nil {
			t.Fatalf("accepted %+v", req)
		}
	}
}

type lbTestAPI struct {
	client    *Client
	objects   map[string]map[string]interface{}
	posts     int
	failKind  string
	endpoints map[string][]map[string]interface{}
}

func newLBTestAPI(t *testing.T) *lbTestAPI {
	t.Helper()
	fake := &lbTestAPI{objects: map[string]map[string]interface{}{}, endpoints: map[string][]map[string]interface{}{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := strings.SplitN(r.URL.Path, "/projects/p/", 2)
		if len(path) != 2 {
			t.Errorf("unexpected path %s", r.URL.Path)
			http.Error(w, "bad path", 400)
			return
		}
		key := path[1]
		if strings.Contains(key, "/operations/") {
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
			return
		}
		if strings.HasPrefix(key, "aggregated/") {
			kind := strings.TrimPrefix(key, "aggregated/")
			items := map[string]interface{}{}
			groups := []map[string]interface{}{}
			for path, obj := range fake.objects {
				if strings.Contains(path, "/"+kind+"/") {
					groups = append(groups, obj)
				}
			}
			if len(groups) > 0 {
				items["zones/r-b"] = map[string]interface{}{kind: groups}
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": items})
			return
		}
		if strings.HasSuffix(key, "/listNetworkEndpoints") {
			base := strings.TrimSuffix(key, "/listNetworkEndpoints")
			items := []map[string]interface{}{}
			for _, ep := range fake.endpoints[base] {
				items = append(items, map[string]interface{}{"networkEndpoint": ep})
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": items})
			return
		}
		if strings.HasSuffix(key, "/getHealth") {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"healthStatus": []interface{}{map[string]interface{}{"ipAddress": "10.0.0.5", "port": 8080, "healthState": "HEALTHY"}}})
			return
		}
		if strings.HasSuffix(key, "/attachNetworkEndpoints") || strings.HasSuffix(key, "/detachNetworkEndpoints") {
			parts := strings.Split(key, "/")
			action := parts[len(parts)-1]
			base := strings.Join(parts[:len(parts)-1], "/")
			body := struct {
				Endpoints []map[string]interface{} `json:"networkEndpoints"`
			}{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if action == "attachNetworkEndpoints" {
				fake.endpoints[base] = body.Endpoints
			} else {
				delete(fake.endpoints, base)
			}
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
			return
		}
		if strings.HasSuffix(key, "/setTags") {
			body := map[string]interface{}{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			fake.objects[strings.TrimSuffix(key, "/setTags")]["tags"] = body
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
			return
		}

		switch r.Method {
		case "GET":
			if key == "global/backendServices" {
				items := []map[string]interface{}{}
				for path, obj := range fake.objects {
					if strings.HasPrefix(path, key+"/") {
						items = append(items, obj)
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"items": items})
				return
			}
			obj, ok := fake.objects[key]
			if !ok {
				http.Error(w, `{"error":{"code":404,"message":"missing"}}`, 404)
				return
			}
			_ = json.NewEncoder(w).Encode(obj)
		case "POST":
			fake.posts++
			if strings.Contains(key, fake.failKind) && fake.failKind != "" {
				http.Error(w, `{"error":{"code":400,"message":"injected"}}`, 400)
				return
			}
			obj := map[string]interface{}{}
			if err := json.NewDecoder(r.Body).Decode(&obj); err != nil {
				t.Error(err)
			}
			name, _ := obj["name"].(string)
			fake.objects[key+"/"+name] = obj
			if strings.Contains(key, "addresses") {
				obj["address"] = "203.0.113.1"
			}
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
		case "PUT", "PATCH":
			body := map[string]interface{}{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.Method == "PUT" {
				fake.objects[key] = body
			} else {
				for k, v := range body {
					fake.objects[key][k] = v
				}
			}
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
		case "DELETE":
			delete(fake.endpoints, key)
			delete(fake.objects, key)
			_, _ = w.Write([]byte(`{"name":"op","status":"DONE"}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	fake.client = &Client{config: &Config{ProjectID: "p", Region: "r", Zone: "r-a", NetworkName: "network"}}
	initLBTestClients(t, fake.client, server.URL)
	return fake
}

func initLBTestClients(t *testing.T, c *Client, endpoint string) {
	t.Helper()
	if err := c.initLoadBalancingClients(context.Background(), []option.ClientOption{option.WithEndpoint(endpoint), option.WithoutAuthentication()}); err != nil {
		t.Fatal(err)
	}
	opts := []option.ClientOption{option.WithEndpoint(endpoint), option.WithoutAuthentication()}
	var err error
	c.instancesClient, err = compute.NewInstancesRESTClient(context.Background(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	c.firewallsClient, err = compute.NewFirewallsRESTClient(context.Background(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	c.clientsLoaded = true
	t.Cleanup(func() { _ = c.Cleanup(context.Background()) })
}

func TestNativeLBBackendLifecycle(t *testing.T) {
	fake := newLBTestAPI(t)
	ctx := context.Background()
	manager := &LoadBalancerManager{client: fake.client}
	fake.objects["zones/r-b/instances/vm"] = map[string]interface{}{"name": "vm", "zone": "projects/p/zones/r-b", "tags": map[string]interface{}{"fingerprint": "original", "items": []string{"keep"}}, "networkInterfaces": []interface{}{map[string]interface{}{"network": "projects/p/global/networks/network", "subnetwork": "projects/p/regions/r/subnetworks/sub", "networkIP": "10.0.0.5"}}}
	lb, err := manager.CreateLoadBalancer(ctx, &cpi.CreateLoadBalancerRequest{Name: "service", Protocol: "tcp", Port: 2222, TargetPort: 8080})
	if err != nil {
		t.Fatal(err)
	}
	backend := &cpi.Backend{Address: "10.0.0.5", Port: 8080, Weight: 1, Enabled: true}
	if err = manager.AddBackend(ctx, lb.ID, backend); err != nil {
		t.Fatal(err)
	}
	members, err := manager.listBackends(ctx, lb.ID)
	if err != nil || len(members) != 1 || !members[0].Enabled {
		t.Fatalf("members=%v err=%v", members, err)
	}
	if err = manager.AddBackend(ctx, lb.ID, backend); err != nil {
		t.Fatal(err)
	}
	health, err := manager.GetHealthStatus(ctx, lb.ID)
	if err != nil || health.Healthy != 1 || health.Total != 1 {
		t.Fatalf("health=%v err=%v", health, err)
	}
	if err = manager.ConfigureHealthCheck(ctx, lb.ID, &cpi.HealthCheck{Protocol: "http", Port: 9090, Path: "/ready"}); err != nil {
		t.Fatal(err)
	}
	for path, object := range fake.objects {
		if strings.HasPrefix(path, "global/firewalls/") {
			allowed := object["allowed"].([]interface{})
			ports := allowed[0].(map[string]interface{})["ports"].([]interface{})
			if len(ports) != 2 || ports[0] != "8080" || ports[1] != "9090" {
				t.Errorf("wrong migrated probe scope %v", ports)
			}
		}
	}
	// A new manager proves disable/enable identity survives process-local state.
	manager = &LoadBalancerManager{client: fake.client}
	if err = manager.DisableBackend(ctx, lb.ID, members[0].ID); err != nil {
		t.Fatal(err)
	}
	members, err = manager.listBackends(ctx, lb.ID)
	if err != nil || members[0].Enabled {
		t.Fatalf("disable failed: %v", err)
	}
	if err = manager.EnableBackend(ctx, lb.ID, members[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = manager.RemoveBackend(ctx, lb.ID, members[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = manager.DeleteLoadBalancer(ctx, lb.ID); err != nil {
		t.Fatal(err)
	}
	if len(fake.objects) != 1 {
		t.Fatalf("leaked graph %+v", fake.objects)
	}
}
func TestNativeLBRollbackAndOwnership(t *testing.T) {
	for _, kind := range []string{"healthChecks", "backendServices", "urlMaps", "targetHttpProxies", "addresses", "forwardingRules"} {
		t.Run(kind, func(t *testing.T) {
			fake := newLBTestAPI(t)
			fake.failKind = kind
			_, err := (&LoadBalancerManager{client: fake.client}).CreateLoadBalancer(context.Background(), &cpi.CreateLoadBalancerRequest{Name: "service", Protocol: "http", Port: 80})
			if err == nil {
				t.Fatal("injected failure swallowed")
			}
			if len(fake.objects) != 0 {
				t.Fatalf("rollback leaked %+v", fake.objects)
			}
		})
	}
	fake := newLBTestAPI(t)
	fake.objects["global/addresses/service-ip"] = map[string]interface{}{"name": "service-ip", "description": "foreign"}
	if _, err := (&LoadBalancerManager{client: fake.client}).CreateLoadBalancer(context.Background(), &cpi.CreateLoadBalancerRequest{Name: "service", Protocol: "http", Port: 80}); err == nil {
		t.Fatal("adopted foreign graph")
	}
	if fake.posts != 0 {
		t.Fatal("mutated before detecting ownership conflict")
	}
}

func TestNativeLBRemoveResumesAfterServiceDetach(t *testing.T) {
	fake := newLBTestAPI(t)
	ctx := context.Background()
	manager := &LoadBalancerManager{client: fake.client}
	fake.objects["zones/r-b/instances/vm"] = map[string]interface{}{"name": "vm", "zone": "projects/p/zones/r-b", "networkInterfaces": []interface{}{map[string]interface{}{"network": "projects/p/global/networks/network", "subnetwork": "projects/p/regions/r/subnetworks/sub", "networkIP": "10.0.0.5"}}}
	lb, err := manager.CreateLoadBalancer(ctx, &cpi.CreateLoadBalancerRequest{Name: "service", Protocol: "tcp", Port: 2222})
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.AddBackend(ctx, lb.ID, &cpi.Backend{Address: "10.0.0.5", Port: 8080, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	fake.objects["global/backendServices/service-bs"]["backends"] = []interface{}{}
	if err = manager.RemoveBackend(ctx, lb.ID, "10.0.0.5"); err != nil {
		t.Fatal(err)
	}
	for path := range fake.objects {
		if strings.Contains(path, "networkEndpointGroups/") || strings.Contains(path, "firewalls/") {
			t.Errorf("orphan survives retry: %s", path)
		}
	}
}

func TestNativeLBRejectsProbeSourceDrift(t *testing.T) {
	fake := newLBTestAPI(t)
	e := &lbEndpoint{ID: "service-endpoint", Network: "projects/p/global/networks/network", Port: 8080}
	fake.objects["global/firewalls/service-endpoint-fw"] = map[string]interface{}{"name": "service-endpoint-fw", "description": lbDescription("service", "firewalls", nil, nil), "network": e.Network, "targetTags": []string{"lb-service-endpoint"}, "direction": "INGRESS", "sourceRanges": []string{"0.0.0.0/0"}, "allowed": []interface{}{map[string]interface{}{"IPProtocol": "tcp", "ports": []string{"8080"}}}}
	manager := &LoadBalancerManager{client: fake.client}
	if err := manager.migrateProbePort(context.Background(), "service", e, 8080, 9090, true); err == nil {
		t.Fatal("accepted world-accessible probe rule")
	}
}
