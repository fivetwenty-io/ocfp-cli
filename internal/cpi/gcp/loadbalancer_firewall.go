package gcp

import (
	"cloud.google.com/go/compute/apiv1/computepb"
	"context"
	"errors"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"slices"
	"strconv"
)

func (m *LoadBalancerManager) endpointTag(e *lbEndpoint) string { return "lb-" + e.ID }
func (m *LoadBalancerManager) ensureEndpointFirewall(ctx context.Context, req *cpi.CreateLoadBalancerRequest, e *lbEndpoint) error {
	project := m.client.getConfig().GetNetworkProject()
	name := e.ID + "-fw"

	ports := []string{strconv.Itoa(e.Port)}
	if req.HealthCheck.Port != e.Port {
		ports = append(ports, strconv.Itoa(req.HealthCheck.Port))
	}

	firewall := &computepb.Firewall{Name: proto(name), Description: proto(lbDescription(req.Name, "firewalls", nil, nil)), Network: proto(e.Network), Direction: proto("INGRESS"), SourceRanges: []string{"35.191.0.0/16", "130.211.0.0/22"}, TargetTags: []string{m.endpointTag(e)}, Allowed: []*computepb.Allowed{{IPProtocol: proto("tcp"), Ports: ports}}}

	existing, err := m.client.firewallsClient.Get(ctx, &computepb.GetFirewallRequest{Project: project, Firewall: name})
	if err == nil {
		if _, err = parseLBOwner(existing.GetDescription(), req.Name, "firewalls"); err != nil {
			return err
		}

		if !lbResourceMatches(existing, firewall) {
			return errors.New("endpoint firewall differs from requested permissions")
		}
	} else {
		if !IsNotFound(WrapGCPError(err, "LoadBalancer.Firewall")) {
			return WrapGCPError(err, "LoadBalancer.Firewall")
		}

		op, err := m.client.firewallsClient.Insert(ctx, &computepb.InsertFirewallRequest{Project: project, FirewallResource: firewall})
		if err = waitLBOperation(ctx, op, err); err != nil {
			return err
		}
	}

	return m.setEndpointTag(ctx, e, true)
}
func (m *LoadBalancerManager) setEndpointTag(ctx context.Context, e *lbEndpoint, enabled bool) error {
	project, zone, name := resourceScope(m.client.getConfig().ProjectID, e.Zone, e.Instance)

	vm, err := m.client.instancesClient.Get(ctx, &computepb.GetInstanceRequest{Project: project, Zone: zone, Instance: name})
	if err != nil {
		wrapped := WrapGCPError(err, "LoadBalancer.BackendTags")
		if !enabled && IsNotFound(wrapped) {
			return nil
		}

		return wrapped
	}

	items := []string{}
	found := false

	tag := m.endpointTag(e)
	for _, item := range vm.GetTags().GetItems() {
		if item == tag {
			found = true

			if !enabled {
				continue
			}
		}

		items = append(items, item)
	}

	if enabled == found {
		return nil
	}

	if enabled {
		items = append(items, tag)
	}

	op, err := m.client.instancesClient.SetTags(ctx, &computepb.SetTagsInstanceRequest{Project: project, Zone: zone, Instance: name, TagsResource: &computepb.Tags{Fingerprint: proto(vm.GetTags().GetFingerprint()), Items: items}})

	return waitLBOperation(ctx, op, err)
}
func (m *LoadBalancerManager) deleteEndpoint(ctx context.Context, name string, e *lbEndpoint) error {
	// Validate both independently before mutating either dependency.
	_, _, err := m.getEndpoint(ctx, name, m.endpointURL(e))
	if err != nil && !IsNotFound(err) {
		return err
	}

	firewall, getErr := m.client.firewallsClient.Get(ctx, &computepb.GetFirewallRequest{Project: m.client.getConfig().GetNetworkProject(), Firewall: e.ID + "-fw"})
	if getErr != nil && !IsNotFound(WrapGCPError(getErr, "LoadBalancer.Firewall")) {
		return getErr
	}

	if getErr == nil {
		if _, err = parseLBOwner(firewall.GetDescription(), name, "firewalls"); err != nil {
			return err
		}
	}

	if getErr == nil {
		op, err := m.client.firewallsClient.Delete(ctx, &computepb.DeleteFirewallRequest{Project: m.client.getConfig().GetNetworkProject(), Firewall: e.ID + "-fw"})
		if err = waitLBOperation(ctx, op, err); err != nil {
			return err
		}
	}

	if err := m.setEndpointTag(ctx, e, false); err != nil {
		return err
	}

	op, deleteErr := m.client.networkEndpointGroupsClient.Delete(ctx, &computepb.DeleteNetworkEndpointGroupRequest{Project: m.client.getConfig().ProjectID, Zone: e.Zone, NetworkEndpointGroup: e.ID})
	if !IsNotFound(WrapGCPError(deleteErr, "LoadBalancer.DeleteEndpoint")) {
		if err = waitLBOperation(ctx, op, deleteErr); err != nil {
			return err
		}
	}

	return nil
}

func (m *LoadBalancerManager) migrateProbePort(ctx context.Context, name string, e *lbEndpoint, oldPort, newPort int, retainOld bool) error {
	project := m.client.getConfig().GetNetworkProject()

	rule, err := m.client.firewallsClient.Get(ctx, &computepb.GetFirewallRequest{Project: project, Firewall: e.ID + "-fw"})
	if err != nil {
		return WrapGCPError(err, "LoadBalancer.ProbeFirewall")
	}

	if _, err = parseLBOwner(rule.GetDescription(), name, "firewalls"); err != nil {
		return err
	}

	if canonicalURL(rule.GetNetwork()) != canonicalURL(e.Network) || len(rule.GetTargetTags()) != 1 || rule.GetTargetTags()[0] != m.endpointTag(e) {
		return errors.New("probe firewall scope drift")
	}

	ranges := append([]string(nil), rule.GetSourceRanges()...)
	slices.Sort(ranges)

	if !slices.Equal(ranges, []string{"130.211.0.0/22", "35.191.0.0/16"}) || rule.GetDirection() != "INGRESS" || len(rule.GetSourceTags()) != 0 || len(rule.GetSourceServiceAccounts()) != 0 {
		return errors.New("probe firewall source drift")
	}

	permitted := map[string]bool{strconv.Itoa(e.Port): true, strconv.Itoa(oldPort): true, strconv.Itoa(newPort): true}

	for _, allow := range rule.GetAllowed() {
		if allow.GetIPProtocol() != "tcp" {
			return errors.New("probe firewall protocol drift")
		}

		for _, port := range allow.GetPorts() {
			if !permitted[port] {
				return errors.New("probe firewall port drift")
			}
		}
	}

	ports := []string{strconv.Itoa(e.Port)}
	if newPort != e.Port {
		ports = append(ports, strconv.Itoa(newPort))
	}

	if retainOld && oldPort != e.Port && oldPort != newPort {
		ports = append(ports, strconv.Itoa(oldPort))
	}

	op, err := m.client.firewallsClient.Patch(ctx, &computepb.PatchFirewallRequest{Project: project, Firewall: e.ID + "-fw", FirewallResource: &computepb.Firewall{Allowed: []*computepb.Allowed{{IPProtocol: proto("tcp"), Ports: ports}}}})

	return waitLBOperation(ctx, op, err)
}
