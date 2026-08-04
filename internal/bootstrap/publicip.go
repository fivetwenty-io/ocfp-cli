package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/ocfp/ocfp-cli-go/internal/ui"
)

// Public IP specific constants.
const (
	defaultJumpboxCount   = 2
	defaultRouterCount    = 4
	defaultTCPRouterCount = 2
)

// ErrPublicIPCreateFailed reports addresses that could not be allocated after
// every retry. The bootstrap runner aborts on it rather than reporting a
// completed step for work that did not happen.
var ErrPublicIPCreateFailed = errors.New("public IP creation failed")

// stackitEnsure is an interface for STACKIT-specific floating IP management.
type stackitEnsure interface {
	EnsureFloatingIP(ctx context.Context, req *cpi.PublicIPRequest) (*cpi.PublicIP, error)
}

// publicIPCapable lets a NetworkManager declare that its public IP methods are
// unimplemented stubs. Providers that manage addresses externally (PVE) still
// satisfy cpi.NetworkManager, so a non-nil manager is not by itself evidence
// that public IPs can be created.
type publicIPCapable interface {
	SupportsPublicIPs() bool
}

// ==============================================================================
// Public IP Creation
// ==============================================================================

// CreatePublicIPs creates public IPs for the bootstrap environment (exported for testing).
func (m *Manager) CreatePublicIPs(ctx context.Context) error {
	if !m.supportsPublicIPs() {
		logger.Infof("Provider %s does not support public IPs, skipping creation", m.options.Provider)

		return nil
	}

	netMgr := m.provider.NetworkManager()
	stackitProvider, isStackit := m.getStackitProvider(netMgr)

	var (
		allIPs []*cpi.PublicIP
		errs   []error
	)

	collect := func(ips []*cpi.PublicIP, err error) {
		allIPs = append(allIPs, ips...)

		if err != nil {
			errs = append(errs, err)
		}
	}

	// Create ops public IPs (all providers)
	collect(m.createOpsPublicIPs(ctx, netMgr))

	if isStackit {
		// STACKIT-specific: Create jumpbox IPs using floating IP method
		collect(m.createJumpboxPublicIPs(ctx, stackitProvider))

		// Create remaining public IPs (STACKIT only - other providers manage these differently)
		collect(m.createRouterPublicIPs(ctx, netMgr))
		collect(m.createCFSSHPublicIPs(ctx, netMgr))
		collect(m.createTCPRouterPublicIPs(ctx, netMgr))
		collect(m.createBastionPublicIPs(ctx, netMgr))
	}

	if len(allIPs) > 0 {
		m.renderPublicIPsSummary(allIPs)
	}

	// Every group is attempted before failing, so one unavailable address does
	// not mask the state of the rest.
	return errors.Join(errs...) //nolint:wrapcheck // already wrapped per group
}

func (m *Manager) supportsPublicIPs() bool {
	netMgr := m.provider.NetworkManager()
	if netMgr == nil {
		return false
	}

	if capable, ok := netMgr.(publicIPCapable); ok {
		return capable.SupportsPublicIPs()
	}

	return true
}

func (m *Manager) getStackitProvider(netMgr cpi.NetworkManager) (stackitEnsure, bool) { //nolint:ireturn // interface type checking
	if m.options.Provider != "stackit" {
		return nil, false
	}

	// Type assertion to check if it supports EnsureFloatingIP
	if se, ok := netMgr.(stackitEnsure); ok {
		return se, true
	}

	return nil, false
}

// ==============================================================================
// Specific Public IP Creation Functions
// ==============================================================================

func (m *Manager) createOpsPublicIPs(ctx context.Context, netMgr cpi.NetworkManager) ([]*cpi.PublicIP, error) {
	count := m.getPublicIPCountWithDefault(m.config.PublicIPs.Ops, 1)

	return m.ensureAndRecordPublicIPs(
		ctx, netMgr, "ops", count,
		"ops-%d", map[string]string{
			"job": "ops",
		},
	)
}

func (m *Manager) createJumpboxPublicIPs(ctx context.Context, stackitProvider stackitEnsure) ([]*cpi.PublicIP, error) {
	count := m.getPublicIPCountWithDefault(m.config.PublicIPs.Jumpbox, defaultJumpboxCount)

	ips := make([]*cpi.PublicIP, 0, count)
	recorded := make([]indexedPublicIP, 0, count)
	failed := make([]string, 0, count)

	for i := range count {
		name := fmt.Sprintf("%s-jumpbox-%d", m.options.BlocName, i)
		labels := map[string]string{
			"job":   "jumpbox",
			"index": strconv.Itoa(i),
			"env":   "mgmt",
		}

		publicIP, err := stackitProvider.EnsureFloatingIP(ctx, &cpi.PublicIPRequest{
			Name:   name,
			Labels: labels,
			Tags:   m.baseTags(),
		})
		if err != nil {
			logger.Errorf("Failed to create jumpbox public IP %s: %v", name, err)

			failed = append(failed, name)

			continue
		}

		ips = append(ips, publicIP)
		recorded = append(recorded, indexedPublicIP{index: i, ip: publicIP})
	}

	m.savePublicIPsToState(recorded, "jumpbox", "jumpbox-%d")

	return ips, publicIPFailure(failed)
}

// publicIPFailure names the addresses that could not be allocated, or returns
// nil when every requested address is accounted for.
func publicIPFailure(failed []string) error {
	if len(failed) == 0 {
		return nil
	}

	return fmt.Errorf("%w: %s", ErrPublicIPCreateFailed, strings.Join(failed, ", "))
}

func (m *Manager) createRouterPublicIPs(ctx context.Context, netMgr cpi.NetworkManager) ([]*cpi.PublicIP, error) {
	count := m.getPublicIPCountWithDefault(m.config.PublicIPs.Router, defaultRouterCount)

	return m.ensureAndRecordPublicIPs(
		ctx, netMgr, "router", count,
		"ocf-cf-router-%d", map[string]string{
			"job": "router",
		},
	)
}

func (m *Manager) createCFSSHPublicIPs(ctx context.Context, netMgr cpi.NetworkManager) ([]*cpi.PublicIP, error) {
	count := m.getPublicIPCountWithDefault(m.config.PublicIPs.CFSSH, 1)

	return m.ensureAndRecordPublicIPs(
		ctx, netMgr, "cf-ssh", count,
		"ocf-cf-ssh-%d", map[string]string{
			"job": "cf-ssh",
		},
	)
}

func (m *Manager) createTCPRouterPublicIPs(ctx context.Context, netMgr cpi.NetworkManager) ([]*cpi.PublicIP, error) {
	count := m.getPublicIPCountWithDefault(m.config.PublicIPs.TCPRouter, defaultTCPRouterCount)

	return m.ensureAndRecordPublicIPs(
		ctx, netMgr, "tcp-router", count,
		"ocf-cf-tcp-router-%d", map[string]string{
			"job": "tcp-router",
		},
	)
}

func (m *Manager) createBastionPublicIPs(ctx context.Context, netMgr cpi.NetworkManager) ([]*cpi.PublicIP, error) {
	// Always create exactly 1 bastion public IP
	return m.ensureAndRecordPublicIPs(
		ctx, netMgr, "bastion", 1,
		"bastion", map[string]string{
			"job":  "bastion",
			"type": "bastion",
		},
	)
}

// ==============================================================================
// Public IP Utility Functions
// ==============================================================================

// getPublicIPCountWithDefault returns the configured count or default if not set (exported for plan module).
func (m *Manager) getPublicIPCountWithDefault(configured, defaultCount int) int {
	if configured > 0 {
		return configured
	}

	return defaultCount
}

func (m *Manager) isBlocPublicIP(publicIP *cpi.PublicIP) bool {
	// Check if the public IP belongs to this bloc
	if blocTag, ok := publicIP.Tags["bloc"]; ok {
		return blocTag == m.options.BlocName
	}

	// Also check if the name starts with the bloc name
	return strings.HasPrefix(publicIP.Name, m.options.BlocName+"-")
}

// ==============================================================================
// Public IP State Management
// ==============================================================================

// indexedPublicIP pairs an address with the slot it was requested for. The slot
// must survive a failed sibling: recording by position in the success slice
// renames every address after a gap.
type indexedPublicIP struct {
	index int
	ip    *cpi.PublicIP
}

func (m *Manager) savePublicIPsToState(ips []indexedPublicIP, job, nameFormat string) {
	for _, entry := range ips {
		index, publicIP := entry.index, entry.ip

		// Format name - if nameFormat contains %d, use index; otherwise use as-is
		var formattedName string
		if strings.Contains(nameFormat, "%") {
			formattedName = fmt.Sprintf(nameFormat, index)
		} else {
			formattedName = nameFormat
		}

		name := fmt.Sprintf("%s-%s", m.options.BlocName, formattedName)

		err := m.stateManager.AddResource(&state.Resource{
			ID:         publicIP.ID,
			Type:       "public_ip",
			Name:       name,
			Provider:   m.options.Provider,
			State:      string(cpi.ResourceStateActive),
			Properties: map[string]interface{}{"ip_address": publicIP.IPAddress},
			Tags:       m.baseTags(),
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
		})
		if err != nil {
			logger.Errorf("Failed to save public IP to state: %v", err)
		}

		// Set outputs
		_ = m.stateManager.SetOutput(fmt.Sprintf("%s_public_ip_%d", job, index), publicIP.IPAddress)
		_ = m.stateManager.SetOutput(fmt.Sprintf("%s_public_ip_%d_id", job, index), publicIP.ID)
	}
}

func (m *Manager) getPublicIPFromState(name string, baseLabels map[string]string) *cpi.PublicIP {
	// Try to get from state first
	if resource, _ := m.stateManager.GetResource("public_ip", name); resource != nil {
		var ipAddress string
		if ip, ok := resource.Properties["ip_address"].(string); ok {
			ipAddress = ip
		}

		return &cpi.PublicIP{
			ID:        resource.ID,
			Name:      name,
			IPAddress: ipAddress, // Set IPAddress for bootstrap code compatibility
			Address:   ipAddress, // Set Address for other code compatibility
			Labels:    baseLabels,
			Tags:      resource.Tags,
		}
	}

	return nil
}

func (m *Manager) ensureAndRecordPublicIPs(
	ctx context.Context,
	netMgr cpi.NetworkManager,
	job string,
	count int,
	nameFormat string,
	baseLabels map[string]string,
) ([]*cpi.PublicIP, error) {
	ips := make([]*cpi.PublicIP, 0, count)
	recorded := make([]indexedPublicIP, 0, count)
	failed := make([]string, 0, count)

	const maxRetriesPerIP = 3

	for index := range count {
		// Format name - if nameFormat contains %d, use index; otherwise use as-is
		var formattedName string
		if strings.Contains(nameFormat, "%") {
			formattedName = fmt.Sprintf(nameFormat, index)
		} else {
			formattedName = nameFormat
		}

		name := fmt.Sprintf("%s-%s", m.options.BlocName, formattedName)

		var ip *cpi.PublicIP
		for attempt := range maxRetriesPerIP {
			ip = m.getOrCreatePublicIP(ctx, netMgr, name, job, index, baseLabels)
			if ip != nil {
				// Success! Add to list and break retry loop
				ips = append(ips, ip)
				recorded = append(recorded, indexedPublicIP{index: index, ip: ip})

				break
			}

			// Failed - log and retry (unless it's the last attempt)
			if attempt < maxRetriesPerIP-1 {
				logger.Warnf("Failed to create public IP %s (attempt %d/%d), retrying...", name, attempt+1, maxRetriesPerIP)
			} else {
				logger.Errorf("Failed to create public IP %s after %d attempts", name, maxRetriesPerIP)

				failed = append(failed, name)
			}
		}
	}

	m.savePublicIPsToState(recorded, job, nameFormat)

	return ips, publicIPFailure(failed)
}

func (m *Manager) getOrCreatePublicIP(
	ctx context.Context,
	netMgr cpi.NetworkManager,
	name, job string,
	index int,
	baseLabels map[string]string,
) *cpi.PublicIP {
	// Check if exists in cloud provider (authoritative source)
	if ip := m.findExistingPublicIP(ctx, netMgr, name, job, index); ip != nil {
		return ip
	}

	// Check if already exists in state (fallback for providers without ListPublicIPs)
	if ip := m.getPublicIPFromState(name, baseLabels); ip != nil {
		return ip
	}

	// Create new public IP
	return m.createPublicIP(ctx, netMgr, name, index, baseLabels)
}

func (m *Manager) findExistingPublicIP(ctx context.Context, netMgr cpi.NetworkManager, name, job string, index int) *cpi.PublicIP {
	publicIPs, err := netMgr.ListPublicIPs(ctx)
	if err != nil {
		logger.Warnf("Failed to list public IPs: %v", err)

		return nil
	}

	return m.matchPublicIP(publicIPs, name, job, index)
}

func (m *Manager) matchPublicIP(publicIPs []*cpi.PublicIP, name, job string, index int) *cpi.PublicIP {
	indexStr := strconv.Itoa(index)

	for _, publicIP := range publicIPs {
		if m.isMatchingPublicIP(publicIP, name, job, indexStr) {
			logger.Infof("Found existing public IP %s (id=%s, address=%s)", name, publicIP.ID, publicIP.IPAddress)

			return publicIP
		}
	}

	return nil
}

func (m *Manager) isMatchingPublicIP(publicIP *cpi.PublicIP, name, job, indexStr string) bool {
	return publicIP.Name == name || (publicIP.Labels["job"] == job && publicIP.Labels["index"] == indexStr)
}

func (m *Manager) createPublicIP(ctx context.Context, netMgr cpi.NetworkManager, name string, index int, baseLabels map[string]string) *cpi.PublicIP {
	logger.Infof("Creating public IP: name=%s", name)
	_, _ = fmt.Fprintf(os.Stdout, "    • Creating public IP %s...\n", name)

	labels := make(map[string]string)
	for k, v := range baseLabels {
		labels[k] = v
	}

	labels["index"] = strconv.Itoa(index)
	labels["name"] = name
	labels["env"] = "mgmt"

	publicIP, err := netMgr.CreatePublicIP(ctx, &cpi.PublicIPRequest{
		Name:   name,
		Labels: labels,
		Tags:   m.baseTags(),
	})
	if err != nil {
		if strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "already exists") {
			logger.Warnf("Public IP %s already exists (conflict), skipping", name)
		} else {
			logger.Errorf("Failed to create public IP %s: %v", name, err)
		}

		return nil
	}

	logger.Infof("Public IP created successfully: id=%s address=%s", publicIP.ID, publicIP.IPAddress)

	return publicIP
}

// ==============================================================================
// Public IP Mapping and Display
// ==============================================================================

func (m *Manager) renderPublicIPsSummary(allIPs []*cpi.PublicIP) {
	logger.Infof("Created %d public IP(s)", len(allIPs))
	// Don't render the table during bootstrap - it's confusing
	// The summary at the end will show what was created
	for _, publicIP := range allIPs {
		if publicIP.IPAddress != "" {
			_, _ = fmt.Fprintf(os.Stdout, "    • %s: %s\n", publicIP.Name, publicIP.IPAddress)
		} else {
			_, _ = fmt.Fprintf(os.Stdout, "    • %s: (allocating...)\n", publicIP.Name)
		}
	}
}

func (m *Manager) showStackitPublicIPsTable(ctx context.Context) {
	netMgr := m.provider.NetworkManager()
	if netMgr == nil {
		return
	}

	publicIPs, err := netMgr.ListPublicIPs(ctx)
	if err != nil {
		logger.Warnf("Failed to list public IPs: %v", err)

		return
	}

	// Filter to only this bloc's IPs
	var blocIPs []*cpi.PublicIP

	for _, publicIP := range publicIPs {
		if m.isBlocPublicIP(publicIP) {
			blocIPs = append(blocIPs, publicIP)
		}
	}

	if len(blocIPs) == 0 {
		return
	}

	_, _ = fmt.Fprintf(os.Stdout, "\n🌐 Public IPs:\n")

	table := ui.NewTable("")
	table.SetHeaders([]string{"Name", "IP Address", "Job"})

	for _, publicIP := range blocIPs {
		job := publicIP.Labels["job"]
		if job == "" {
			job = "unknown"
		}

		table.AddRow([]string{publicIP.Name, publicIP.IPAddress, job})
	}

	_ = table.Render()
}
