package bootstrap

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/fivetwenty-io/ocfp-cli/internal/bastion/ssh"
	"github.com/fivetwenty-io/ocfp-cli/internal/cpi"
	"github.com/fivetwenty-io/ocfp-cli/internal/logger"
)

// knownHostsFile locates the known_hosts file the bastion SSH client checks.
// Tests point it at a temporary file so they never touch the real one.
var knownHostsFile = ssh.KnownHostsFile

// forgetBastionHostKeys removes the known_hosts entries for every address of
// a bastion this run has just created. It is never called for a bastion that
// already existed and was adopted, whose keys are the ones on record.
//
// A new bastion has new host keys, but it often takes over the addresses of
// the machine it replaces: the same static private IP, the bloc's reserved
// public IP, the operator's bastion_ip, and the host named by
// PVE_BASTION_IP or AWS_BASTION_IP. The entries that machine left
// would make the strict host key check refuse the new one, so they go now,
// on purpose, and the client records the new key on first contact. Both
// fingerprints are logged: the old ones here, the new one when it is
// recorded. A failure only warns, since the bastion exists and a later
// connection reports any entry left behind with the command that clears it.
func (m *Manager) forgetBastionHostKeys(instance *cpi.Instance, bastionName string) {
	knownHostsPath, err := knownHostsFile()
	if err != nil {
		logger.Warnf("Could not locate known_hosts to clear old host keys for bastion %s: %v", bastionName, err)

		return
	}

	for _, host := range m.bastionAddresses(instance, bastionName) {
		removed, err := ssh.RemoveKnownHost(knownHostsPath, host, ssh.DefaultSSHPort)
		if err != nil {
			logger.Warnf("Could not clear old host keys for bastion address %s from %s: %v", host, knownHostsPath, err)

			continue
		}

		for _, entry := range removed {
			logger.Infof("Removed old host key %s for bastion address %s from %s line %d; the new bastion's key is recorded on first connection",
				entry.Fingerprint, host, knownHostsPath, entry.Line)
		}

		if len(removed) > 0 {
			_, _ = fmt.Fprintf(os.Stdout, "    • Cleared %d old host key entr%s for %s from %s\n",
				len(removed), pluralEntries(len(removed)), host, knownHostsPath)
		}
	}
}

// bastionAddresses lists the distinct addresses an operator or ocfp may use
// to reach the bastion.
func (m *Manager) bastionAddresses(instance *cpi.Instance, bastionName string) []string {
	candidates := []string{}

	if instance != nil {
		candidates = append(candidates, instance.PublicIP, instance.FloatingIP, instance.PrivateIP)
	}

	if publicIP, _ := m.stateManager.GetResource("public_ip", bastionName); publicIP != nil {
		if address, ok := publicIP.Properties["ip_address"].(string); ok {
			candidates = append(candidates, address)
		}
	}

	// STACKIT records the reserved IP it found by label, and attached
	// after the instance was read, only as this output.
	if output, _ := m.stateManager.GetOutput("bastion_public_ip"); output != nil {
		if address, ok := output.(string); ok {
			candidates = append(candidates, address)
		}
	}

	if m.config != nil {
		candidates = append(candidates, m.config.BastionIP)
	}

	// The bastion commands fall back to these variables to find the host
	// they dial; PVE's often holds a Tailscale name.
	if m.options != nil {
		switch strings.ToLower(m.options.Provider) {
		case "pve":
			candidates = append(candidates, os.Getenv("PVE_BASTION_IP"))
		case "aws":
			candidates = append(candidates, os.Getenv("AWS_BASTION_IP"))
		}
	}

	addresses := make([]string, 0, len(candidates))

	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && !slices.Contains(addresses, candidate) {
			addresses = append(addresses, candidate)
		}
	}

	return addresses
}

func pluralEntries(n int) string {
	if n == 1 {
		return "y"
	}

	return "ies"
}
