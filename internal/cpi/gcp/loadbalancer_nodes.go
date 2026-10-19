package gcp

import (
	compute "cloud.google.com/go/compute/apiv1"
	"cloud.google.com/go/compute/apiv1/computepb"
	"context"
	"encoding/json"
	"google.golang.org/protobuf/encoding/protojson"
	gproto "google.golang.org/protobuf/proto"
	"reflect"
	"strings"
)

type lbNode struct {
	kind, name string
	want       gproto.Message
	read       func(context.Context) (gproto.Message, error)
	insert     func(context.Context) (*compute.Operation, error)
	remove     func(context.Context) (*compute.Operation, error)
}

func waitLBOperation(ctx context.Context, op *compute.Operation, err error) error {
	if err != nil {
		return WrapGCPError(err, "LoadBalancer")
	}

	return WrapGCPError(op.Wait(ctx), "LoadBalancer.Wait")
}
func (n lbNode) delete(ctx context.Context) error {
	op, err := n.remove(ctx)
	if IsNotFound(WrapGCPError(err, "LoadBalancer.Delete")) {
		return nil
	}

	return waitLBOperation(ctx, op, err)
}
func lbResourceMatches(actual, want gproto.Message) bool {
	a, err := protojson.Marshal(actual)
	if err != nil {
		return false
	}

	b, err := protojson.Marshal(want)
	if err != nil {
		return false
	}

	var am, bm map[string]interface{}
	if json.Unmarshal(a, &am) != nil || json.Unmarshal(b, &bm) != nil {
		return false
	}
	// Native reads return the reserved address number instead of its create URL.
	delete(bm, "IPAddress")
	// Dynamic backend membership is reconciled independently.
	delete(bm, "backends")

	return lbJSONSubset(am, bm)
}
func lbJSONSubset(actual, want interface{}) bool {
	switch value := want.(type) {
	case map[string]interface{}:
		got, ok := actual.(map[string]interface{})
		if !ok {
			return false
		}

		for k, v := range value {
			if !lbJSONSubset(got[k], v) {
				return false
			}
		}

		return true
	case []interface{}:
		got, ok := actual.([]interface{})
		if !ok || len(got) != len(value) {
			return false
		}

		for i, v := range value {
			if !lbJSONSubset(got[i], v) {
				return false
			}
		}

		return true
	case string:
		got, ok := actual.(string)

		return ok && strings.TrimPrefix(got, "https://www.googleapis.com/compute/v1/") == strings.TrimPrefix(value, "https://www.googleapis.com/compute/v1/")
	default:
		return reflect.DeepEqual(actual, want)
	}
}
func lbDescriptionOf(object gproto.Message) string {
	if described, ok := object.(interface{ GetDescription() string }); ok {
		return described.GetDescription()
	}

	return ""
}
func (m *LoadBalancerManager) nodeHealthCheck(resource *computepb.HealthCheck) lbNode {
	project := m.client.getConfig().ProjectID
	name := resource.GetName()
	client := m.client.healthChecksClient

	return lbNode{kind: "healthChecks", name: name, want: resource,
		read: func(ctx context.Context) (gproto.Message, error) {
			return client.Get(ctx, &computepb.GetHealthCheckRequest{Project: project, HealthCheck: name})
		},
		insert: func(ctx context.Context) (*compute.Operation, error) {
			return client.Insert(ctx, &computepb.InsertHealthCheckRequest{Project: project, HealthCheckResource: resource})
		},
		remove: func(ctx context.Context) (*compute.Operation, error) {
			return client.Delete(ctx, &computepb.DeleteHealthCheckRequest{Project: project, HealthCheck: name})
		}}
}
func (m *LoadBalancerManager) nodeBackendService(resource *computepb.BackendService) lbNode {
	project := m.client.getConfig().ProjectID
	name := resource.GetName()
	client := m.client.backendServicesClient

	return lbNode{kind: "backendServices", name: name, want: resource,
		read: func(ctx context.Context) (gproto.Message, error) {
			return client.Get(ctx, &computepb.GetBackendServiceRequest{Project: project, BackendService: name})
		},
		insert: func(ctx context.Context) (*compute.Operation, error) {
			return client.Insert(ctx, &computepb.InsertBackendServiceRequest{Project: project, BackendServiceResource: resource})
		},
		remove: func(ctx context.Context) (*compute.Operation, error) {
			return client.Delete(ctx, &computepb.DeleteBackendServiceRequest{Project: project, BackendService: name})
		}}
}
func (m *LoadBalancerManager) nodeUrlMap(resource *computepb.UrlMap) lbNode {
	project := m.client.getConfig().ProjectID
	name := resource.GetName()
	client := m.client.urlMapsClient

	return lbNode{kind: "urlMaps", name: name, want: resource,
		read: func(ctx context.Context) (gproto.Message, error) {
			return client.Get(ctx, &computepb.GetUrlMapRequest{Project: project, UrlMap: name})
		},
		insert: func(ctx context.Context) (*compute.Operation, error) {
			return client.Insert(ctx, &computepb.InsertUrlMapRequest{Project: project, UrlMapResource: resource})
		},
		remove: func(ctx context.Context) (*compute.Operation, error) {
			return client.Delete(ctx, &computepb.DeleteUrlMapRequest{Project: project, UrlMap: name})
		}}
}
func (m *LoadBalancerManager) nodeTargetHttpProxy(resource *computepb.TargetHttpProxy) lbNode {
	project := m.client.getConfig().ProjectID
	name := resource.GetName()
	client := m.client.targetHttpProxiesClient

	return lbNode{kind: "targetHttpProxies", name: name, want: resource,
		read: func(ctx context.Context) (gproto.Message, error) {
			return client.Get(ctx, &computepb.GetTargetHttpProxyRequest{Project: project, TargetHttpProxy: name})
		},
		insert: func(ctx context.Context) (*compute.Operation, error) {
			return client.Insert(ctx, &computepb.InsertTargetHttpProxyRequest{Project: project, TargetHttpProxyResource: resource})
		},
		remove: func(ctx context.Context) (*compute.Operation, error) {
			return client.Delete(ctx, &computepb.DeleteTargetHttpProxyRequest{Project: project, TargetHttpProxy: name})
		}}
}
func (m *LoadBalancerManager) nodeTargetTcpProxy(resource *computepb.TargetTcpProxy) lbNode {
	project := m.client.getConfig().ProjectID
	name := resource.GetName()
	client := m.client.targetTcpProxiesClient

	return lbNode{kind: "targetTcpProxies", name: name, want: resource,
		read: func(ctx context.Context) (gproto.Message, error) {
			return client.Get(ctx, &computepb.GetTargetTcpProxyRequest{Project: project, TargetTcpProxy: name})
		},
		insert: func(ctx context.Context) (*compute.Operation, error) {
			return client.Insert(ctx, &computepb.InsertTargetTcpProxyRequest{Project: project, TargetTcpProxyResource: resource})
		},
		remove: func(ctx context.Context) (*compute.Operation, error) {
			return client.Delete(ctx, &computepb.DeleteTargetTcpProxyRequest{Project: project, TargetTcpProxy: name})
		}}
}
func (m *LoadBalancerManager) nodeAddress(resource *computepb.Address) lbNode {
	project := m.client.getConfig().ProjectID
	name := resource.GetName()
	client := m.client.globalAddressesClient

	return lbNode{kind: "addresses", name: name, want: resource,
		read: func(ctx context.Context) (gproto.Message, error) {
			return client.Get(ctx, &computepb.GetGlobalAddressRequest{Project: project, Address: name})
		},
		insert: func(ctx context.Context) (*compute.Operation, error) {
			return client.Insert(ctx, &computepb.InsertGlobalAddressRequest{Project: project, AddressResource: resource})
		},
		remove: func(ctx context.Context) (*compute.Operation, error) {
			return client.Delete(ctx, &computepb.DeleteGlobalAddressRequest{Project: project, Address: name})
		}}
}
func (m *LoadBalancerManager) nodeForwardingRule(resource *computepb.ForwardingRule) lbNode {
	project := m.client.getConfig().ProjectID
	name := resource.GetName()
	client := m.client.globalForwardingRulesClient

	return lbNode{kind: "forwardingRules", name: name, want: resource,
		read: func(ctx context.Context) (gproto.Message, error) {
			return client.Get(ctx, &computepb.GetGlobalForwardingRuleRequest{Project: project, ForwardingRule: name})
		},
		insert: func(ctx context.Context) (*compute.Operation, error) {
			return client.Insert(ctx, &computepb.InsertGlobalForwardingRuleRequest{Project: project, ForwardingRuleResource: resource})
		},
		remove: func(ctx context.Context) (*compute.Operation, error) {
			return client.Delete(ctx, &computepb.DeleteGlobalForwardingRuleRequest{Project: project, ForwardingRule: name})
		}}
}
