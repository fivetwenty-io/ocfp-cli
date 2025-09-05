package stackit

import (
	"context"
	"fmt"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	iaas "github.com/stackitcloud/stackit-sdk-go/services/iaas"
	lb "github.com/stackitcloud/stackit-sdk-go/services/loadbalancer"
)

// ComputeManager handles STACKIT compute operations
type ComputeManager struct {
	client *Client
}

// StorageManager handles STACKIT storage operations
type StorageManager struct {
	client *Client
}

// SecurityManager handles STACKIT security operations
type SecurityManager struct {
	client *Client
}

func (m *SecurityManager) CreateSecurityGroup(ctx context.Context, req *cpi.CreateSecurityGroupRequest) (*cpi.SecurityGroup, error) {
	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}
	payload := iaas.NewCreateSecurityGroupPayload(req.Name)
	if req.Description != "" {
		payload.SetDescription(req.Description)
	}
	if len(req.Tags) > 0 {
		lm := make(map[string]interface{}, len(req.Tags))
		for k, v := range req.Tags {
			lm[k] = v
		}
		payload.SetLabels(lm)
	}
	created, err := cli.CreateSecurityGroup(ctx, m.client.config.ProjectID).CreateSecurityGroupPayload(*payload).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas CreateSecurityGroup failed: %w", err)
	}
	out := &cpi.SecurityGroup{ID: stringOrEmpty(created.GetIdOk()), Name: stringOrEmpty(created.GetNameOk()), Description: stringOrEmpty(created.GetDescriptionOk()), Tags: mapAnyToString(created.GetLabels())}
	// Add initial rules if provided (best-effort)
	for _, r := range req.Rules {
		_ = m.AddSecurityRule(ctx, out.ID, r)
	}
	return out, nil
}

func (m *SecurityManager) GetSecurityGroup(ctx context.Context, groupID string) (*cpi.SecurityGroup, error) {
	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}
	sg, err := cli.GetSecurityGroup(ctx, m.client.config.ProjectID, groupID).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas GetSecurityGroup failed: %w", err)
	}
	out := &cpi.SecurityGroup{ID: stringOrEmpty(sg.GetIdOk()), Name: stringOrEmpty(sg.GetNameOk()), Description: stringOrEmpty(sg.GetDescriptionOk()), Tags: mapAnyToString(sg.GetLabels())}
	return out, nil
}

func (m *SecurityManager) ListSecurityGroups(ctx context.Context, filters map[string]string) ([]*cpi.SecurityGroup, error) {
	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}
	resp, err := cli.ListSecurityGroups(ctx, m.client.config.ProjectID).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas ListSecurityGroups failed: %w", err)
	}
	items, _ := resp.GetItemsOk()
	var out []*cpi.SecurityGroup
	for _, sg := range items {
		labels := mapAnyToString(sg.GetLabels())
		g := &cpi.SecurityGroup{ID: stringOrEmpty(sg.GetIdOk()), Name: stringOrEmpty(sg.GetNameOk()), Description: stringOrEmpty(sg.GetDescriptionOk()), Tags: labels}
		if matchLabels(labels, filters) {
			out = append(out, g)
		}
	}
	return out, nil
}

func (m *SecurityManager) DeleteSecurityGroup(ctx context.Context, groupID string) error {
	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}
	return cli.DeleteSecurityGroup(ctx, m.client.config.ProjectID, groupID).Execute()
}

func (m *SecurityManager) AddSecurityRule(ctx context.Context, groupID string, rule *cpi.SecurityRule) error {
	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}
	payload := iaas.NewCreateSecurityGroupRulePayload(rule.Direction)
	payload.SetSecurityGroupId(groupID)
	if rule.Protocol != "" {
		p := rule.Protocol
		payload.SetProtocol(iaas.StringAsCreateProtocol(&p))
	}
	if rule.Description != "" {
		payload.SetDescription(rule.Description)
	}
	if rule.RemoteIPCIDR != "" {
		payload.SetIpRange(rule.RemoteIPCIDR)
	}
	if rule.PortRangeMin > 0 || rule.PortRangeMax > 0 {
		pr := iaas.NewPortRange(int64(rule.PortRangeMax), int64(rule.PortRangeMin))
		payload.SetPortRange(*pr)
	}
	if rule.RemoteGroup != "" {
		payload.SetRemoteSecurityGroupId(rule.RemoteGroup)
	}
	_, err = cli.CreateSecurityGroupRule(ctx, m.client.config.ProjectID, groupID).CreateSecurityGroupRulePayload(*payload).Execute()
	return err
}

func (m *SecurityManager) RemoveSecurityRule(ctx context.Context, groupID string, ruleID string) error {
	cli, err := m.client.getIAASClient()
	if err != nil {
		return err
	}
	return cli.DeleteSecurityGroupRule(ctx, m.client.config.ProjectID, groupID, ruleID).Execute()
}

func (m *SecurityManager) ListSecurityRules(ctx context.Context, groupID string) ([]*cpi.SecurityRule, error) {
	cli, err := m.client.getIAASClient()
	if err != nil {
		return nil, err
	}
	resp, err := cli.ListSecurityGroupRules(ctx, m.client.config.ProjectID, groupID).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit iaas ListSecurityGroupRules failed: %w", err)
	}
	items, _ := resp.GetItemsOk()
	var out []*cpi.SecurityRule
	for _, rule := range items {
		proto := ""
		if pv, ok := rule.GetProtocolOk(); ok {
			proto = fmt.Sprint(pv)
		}
		securityRule := &cpi.SecurityRule{ID: stringOrEmpty(rule.GetIdOk()), Direction: stringOrEmpty(rule.GetDirectionOk()), Protocol: proto, Description: stringOrEmpty(rule.GetDescriptionOk())}
		if pr, ok := rule.GetPortRangeOk(); ok {
			if min, okm := pr.GetMinOk(); okm {
				securityRule.PortRangeMin = int(min)
			}
			if max, okx := pr.GetMaxOk(); okx {
				securityRule.PortRangeMax = int(max)
			}
		}
		if cidr, ok := rule.GetIpRangeOk(); ok {
			securityRule.RemoteIPCIDR = cidr
		}
		if rg, ok := rule.GetRemoteSecurityGroupIdOk(); ok {
			securityRule.RemoteGroup = rg
		}
		out = append(out, securityRule)
	}
	return out, nil
}

// LoadBalancerManager handles STACKIT load balancer operations
type LoadBalancerManager struct {
	client *Client
}

func (m *LoadBalancerManager) CreateLoadBalancer(ctx context.Context, req *cpi.CreateLoadBalancerRequest) (*cpi.LoadBalancer, error) {
	cli, err := m.client.getLoadBalancerClient()
	if err != nil {
		return nil, err
	}
	payload := lb.NewCreateLoadBalancerPayload()
	if req.Name != "" {
		payload.SetName(req.Name)
	}
	// Minimal creation; listener/network details can be updated later
	created, err := cli.CreateLoadBalancer(ctx, m.client.config.ProjectID, m.client.config.Region).CreateLoadBalancerPayload(*payload).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit lb CreateLoadBalancer failed: %w", err)
	}
	out := &cpi.LoadBalancer{ID: stringOrEmpty(created.GetNameOk()), Name: stringOrEmpty(created.GetNameOk())}
	if ip, ok := created.GetExternalAddressOk(); ok {
		out.IPAddress = ip
	} else if pvt, ok := created.GetPrivateAddressOk(); ok {
		out.IPAddress = pvt
	}
	return out, nil
}

func (m *LoadBalancerManager) GetLoadBalancer(ctx context.Context, loadBalancerID string) (*cpi.LoadBalancer, error) {
	cli, err := m.client.getLoadBalancerClient()
	if err != nil {
		return nil, err
	}
	got, err := cli.GetLoadBalancer(ctx, m.client.config.ProjectID, m.client.config.Region, loadBalancerID).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit lb GetLoadBalancer failed: %w", err)
	}
	out := &cpi.LoadBalancer{ID: stringOrEmpty(got.GetNameOk()), Name: stringOrEmpty(got.GetNameOk())}
	if ip, ok := got.GetExternalAddressOk(); ok {
		out.IPAddress = ip
	} else if pvt, ok := got.GetPrivateAddressOk(); ok {
		out.IPAddress = pvt
	}
	if ls, ok := got.GetListenersOk(); ok && len(ls) > 0 {
		if port, okp := ls[0].GetPortOk(); okp {
			out.Port = int(port)
		}
		if proto, okr := ls[0].GetProtocolOk(); okr {
			out.Protocol = string(proto)
		}
	}
	return out, nil
}

func (m *LoadBalancerManager) ListLoadBalancers(ctx context.Context, filters map[string]string) ([]*cpi.LoadBalancer, error) {
	cli, err := m.client.getLoadBalancerClient()
	if err != nil {
		return nil, err
	}
	resp, err := cli.ListLoadBalancers(ctx, m.client.config.ProjectID, m.client.config.Region).Execute()
	if err != nil {
		return nil, fmt.Errorf("stackit lb ListLoadBalancers failed: %w", err)
	}
	items, _ := resp.GetLoadBalancersOk()
	var list []*cpi.LoadBalancer
	for _, lbm := range items {
		lbOut := &cpi.LoadBalancer{ID: stringOrEmpty(lbm.GetNameOk()), Name: stringOrEmpty(lbm.GetNameOk())}
		if ip, ok := lbm.GetExternalAddressOk(); ok {
			lbOut.IPAddress = ip
		} else if pvt, ok := lbm.GetPrivateAddressOk(); ok {
			lbOut.IPAddress = pvt
		}
		list = append(list, lbOut)
	}
	return list, nil
}

func (m *LoadBalancerManager) UpdateLoadBalancer(ctx context.Context, loadBalancerID string, req *cpi.UpdateLoadBalancerRequest) error {
	cli, err := m.client.getLoadBalancerClient()
	if err != nil {
		return err
	}
	payload := lb.NewUpdateLoadBalancerPayload()
	// No-op updates for now; extend when CPI includes listener fields
	_, err = cli.UpdateLoadBalancer(ctx, m.client.config.ProjectID, m.client.config.Region, loadBalancerID).UpdateLoadBalancerPayload(*payload).Execute()
	return err
}

func (m *LoadBalancerManager) DeleteLoadBalancer(ctx context.Context, loadBalancerID string) error {
	cli, err := m.client.getLoadBalancerClient()
	if err != nil {
		return err
	}
	_, err = cli.DeleteLoadBalancer(ctx, m.client.config.ProjectID, m.client.config.Region, loadBalancerID).Execute()
	return err
}

func (m *LoadBalancerManager) AddBackend(ctx context.Context, lbID string, backend *cpi.Backend) error {
	cli, err := m.client.getLoadBalancerClient()
	if err != nil {
		return err
	}
	// Fetch LB to inspect target pools
	got, err := cli.GetLoadBalancer(ctx, m.client.config.ProjectID, m.client.config.Region, lbID).Execute()
	if err != nil {
		return fmt.Errorf("stackit lb GetLoadBalancer failed: %w", err)
	}
	// Choose a default pool name
	poolName := lbID
	// Build or update the pool
	// If pool exists, update it; else create it by updating the LB with new target pool list
	var pools []lb.TargetPool
	if existing, ok := got.GetTargetPoolsOk(); ok {
		pools = existing
	}
	// Try to find existing pool
	var idx = -1
	for i, p := range pools {
		if name, ok := p.GetNameOk(); ok && name == poolName {
			idx = i
			break
		}
	}
	if idx == -1 {
		// Create a new pool entry with targetPort from backend.Port
		newPool := lb.NewTargetPool()
		newPool.SetName(poolName)
		if backend.Port > 0 {
			newPool.SetTargetPort(int64(backend.Port))
		}
		tgt := lb.NewTarget()
		tgt.SetIp(backend.Address)
		if backend.Name != "" {
			tgt.SetDisplayName(backend.Name)
		}
		newPool.SetTargets([]lb.Target{*tgt})
		// Update LB with the new pool
		up := lb.NewUpdateLoadBalancerPayload()
		up.SetTargetPools(append(pools, *newPool))
		_, err = cli.UpdateLoadBalancer(ctx, m.client.config.ProjectID, m.client.config.Region, lbID).UpdateLoadBalancerPayload(*up).Execute()
		return err
	}
	// Pool exists: append or replace target list with added target
	p := pools[idx]
	// Collect current targets
	list := []lb.Target{}
	if t, ok := p.GetTargetsOk(); ok {
		list = t
	}
	// Avoid duplicates by IP
	for _, t := range list {
		if ip, ok := t.GetIpOk(); ok && ip == backend.Address {
			return nil
		}
	}
	nt := lb.NewTarget()
	nt.SetIp(backend.Address)
	if backend.Name != "" {
		nt.SetDisplayName(backend.Name)
	}
	list = append(list, *nt)
	// Update target pool via UpdateTargetPool
	up := lb.NewUpdateTargetPoolPayload()
	if backend.Port > 0 {
		up.SetTargetPort(int64(backend.Port))
	}
	up.SetTargets(list)
	_, err = cli.UpdateTargetPool(ctx, m.client.config.ProjectID, m.client.config.Region, lbID, poolName).UpdateTargetPoolPayload(*up).Execute()
	return err
}

func (m *LoadBalancerManager) RemoveBackend(ctx context.Context, lbID string, backendID string) error {
	cli, err := m.client.getLoadBalancerClient()
	if err != nil {
		return err
	}
	got, err := cli.GetLoadBalancer(ctx, m.client.config.ProjectID, m.client.config.Region, lbID).Execute()
	if err != nil {
		return fmt.Errorf("stackit lb GetLoadBalancer failed: %w", err)
	}
	poolName := lbID
	pools, _ := got.GetTargetPoolsOk()
	var idx = -1
	for i, p := range pools {
		if name, ok := p.GetNameOk(); ok && name == poolName {
			idx = i
			break
		}
	}
	if idx == -1 {
		return nil
	}
	p := pools[idx]
	list := []lb.Target{}
	if t, ok := p.GetTargetsOk(); ok {
		list = t
	}
	// Filter out by IP (use backendID as IP for mapping)
	out := make([]lb.Target, 0, len(list))
	for _, t := range list {
		if ip, ok := t.GetIpOk(); !ok || ip != backendID {
			out = append(out, t)
		}
	}
	up := lb.NewUpdateTargetPoolPayload()
	up.SetTargets(out)
	_, err = cli.UpdateTargetPool(ctx, m.client.config.ProjectID, m.client.config.Region, lbID, poolName).UpdateTargetPoolPayload(*up).Execute()
	return err
}

func (m *LoadBalancerManager) EnableBackend(ctx context.Context, lbID string, backendID string) error {
	return fmt.Errorf("stackit: EnableBackend not yet implemented")
}

func (m *LoadBalancerManager) DisableBackend(ctx context.Context, lbID string, backendID string) error {
	return fmt.Errorf("stackit: DisableBackend not yet implemented")
}

func (m *LoadBalancerManager) ConfigureHealthCheck(ctx context.Context, lbID string, check *cpi.HealthCheck) error {
	cli, err := m.client.getLoadBalancerClient()
	if err != nil {
		return err
	}
	healthCheck := lb.NewActiveHealthCheck()
	if check.HealthyThreshold > 0 {
		healthCheck.SetHealthyThreshold(int64(check.HealthyThreshold))
	}
	if check.UnhealthyThreshold > 0 {
		healthCheck.SetUnhealthyThreshold(int64(check.UnhealthyThreshold))
	}
	if check.Interval > 0 {
		healthCheck.SetInterval(fmt.Sprintf("%ds", check.Interval))
	}
	if check.Timeout > 0 {
		healthCheck.SetTimeout(fmt.Sprintf("%ds", check.Timeout))
	}
	up := lb.NewUpdateTargetPoolPayload()
	up.SetActiveHealthCheck(*healthCheck)
	poolName := lbID
	_, err = cli.UpdateTargetPool(ctx, m.client.config.ProjectID, m.client.config.Region, lbID, poolName).UpdateTargetPoolPayload(*up).Execute()
	return err
}

func (m *LoadBalancerManager) GetHealthStatus(ctx context.Context, lbID string) (*cpi.HealthStatus, error) {
	return nil, fmt.Errorf("stackit: GetHealthStatus not yet implemented")
}
