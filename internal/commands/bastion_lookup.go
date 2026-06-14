package commands

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/spf13/viper"
)

// bastionReachabilityProbeTimeout bounds the TCP probe we use to validate
// cached bastion addresses. Long enough to handle LAN latency, short enough
// that a stale cache entry doesn't make `ocfp ssh bastion` feel hung when
// it falls through to provider discovery.
const bastionReachabilityProbeTimeout = 2 * time.Second

// findBastionIP attempts to discover the bastion IP using multiple label strategies
// and a final name-based fallback for compatibility with Perl tooling.
func findBastionIP(ctx context.Context, provider cpi.Provider, blocName string) (string, error) {
	log := logger.WithOperation("findBastionIP")

	// Explicit operator override wins. The bloc config's bastion_ip is the
	// documented way to reach a bastion whose provider-side address is not
	// routable from the operator (PVE bridge-mode SDN IP unreachable from the
	// Mac; the tailscale IP is the real entry point). Provider discovery below
	// only ever yields the private SDN address, so honour the override first —
	// matching the bastion-init resolution path.
	if ip := configBastionIP(blocName, log); ip != "" {
		log.Debugf("Using bastion_ip override from config: %s", ip)

		return ip, nil
	}

	// Try local state cache first, but only trust it when the bastion is
	// actually reachable. Bootstrap records the *requested* static IP at
	// VM-create time; on PVE templates with predictable interface names
	// the guest may end up on a DHCP lease instead, leaving the cached
	// value stale. A 2s TCP probe catches that case and forces us to fall
	// through to provider-side discovery (QGA / reserved IP) which sees
	// the bastion's real address.
	if ip, found := tryStateCache(blocName, log); found {
		if isBastionReachable(ctx, ip) {
			return ip, nil
		}

		log.Debugf("State-cache bastion IP %s did not answer on port 22 within %s; falling back to provider discovery", ip, bastionReachabilityProbeTimeout)
	}

	// Delegate to instance-level discovery and extract the IP
	inst, err := findBastionInstance(ctx, provider, blocName)
	if err != nil {
		return "", err
	}

	// Check for a direct public/floating IP on the instance
	if publicIP := firstNonEmpty(inst.FloatingIP, inst.PublicIP); publicIP != "" {
		log.Debugf("Found bastion IP from instance %s: %s", inst.Name, publicIP)
		cacheBastionIP(blocName, publicIP)

		return publicIP, nil
	}

	// Check floating IPs associated by instance ID
	fips, err := provider.NetworkManager().ListFloatingIPs(ctx, nil)
	if err == nil {
		for _, fip := range fips {
			if fip.InstanceID == inst.ID && fip.Address != "" {
				log.Debugf("Found bastion floating IP for instance %s: %s", inst.Name, fip.Address)
				cacheBastionIP(blocName, fip.Address)

				return fip.Address, nil
			}
		}
	}

	// Last resort: providers without a public-IP primitive (PVE bridge mode,
	// on-prem deployments reachable via VPN/Tailscale) expose the bastion on
	// its private address. The instance returned by findBastionInstance is
	// the lightweight ListInstances form which won't carry IPs; fetch the
	// detailed record so we can read whatever the provider knows.
	detailed, getErr := provider.ComputeManager().GetInstance(ctx, inst.ID)
	if getErr != nil {
		log.Debugf("GetInstance(%s) failed during bastion lookup: %v", inst.ID, getErr)
	} else if detailed != nil {
		log.Debugf("GetInstance(%s) returned PrivateIP=%q PublicIP=%q FloatingIP=%q",
			inst.ID, detailed.PrivateIP, detailed.PublicIP, detailed.FloatingIP)

		if publicIP := firstNonEmpty(detailed.FloatingIP, detailed.PublicIP); publicIP != "" {
			log.Debugf("Found bastion public IP via GetInstance %s: %s", inst.Name, publicIP)
			cacheBastionIP(blocName, publicIP)

			return publicIP, nil
		}

		if privateIP := firstNonEmpty(detailed.PrivateIP, inst.PrivateIP); privateIP != "" {
			log.Debugf("Found bastion private IP for instance %s: %s", inst.Name, privateIP)
			cacheBastionIP(blocName, privateIP)

			return privateIP, nil
		}
	}

	if inst.PrivateIP != "" {
		log.Debugf("Falling back to bastion private IP from instance %s: %s", inst.Name, inst.PrivateIP)
		cacheBastionIP(blocName, inst.PrivateIP)

		return inst.PrivateIP, nil
	}

	// Last-resort: the bootstrap reserves a static address for the bastion
	// in the bloc's primary subnet and records it as
	// reserved_<subnet>_bastion_ip. Use it when no provider-side IP is
	// available (typical when the guest agent hasn't reported yet on a
	// fresh PVE VM started with DHCP).
	if reservedIP := tryReservedBastionIP(blocName, log); reservedIP != "" {
		log.Debugf("Found bastion IP from reserved-IP state output: %s", reservedIP)
		cacheBastionIP(blocName, reservedIP)

		return reservedIP, nil
	}

	return "", ErrNoBastionHostFound(blocName)
}

// findBastionInstance discovers the bastion instance using label-based strategies
// followed by name-based pattern matching. It returns the *cpi.Instance directly,
// which is useful when callers need the full instance (e.g., configure commands).
func findBastionInstance(ctx context.Context, provider cpi.Provider, blocName string) (*cpi.Instance, error) {
	log := logger.WithOperation("findBastionInstance")

	// Try label-based discovery strategies
	if inst := tryLabelBasedInstanceDiscovery(ctx, provider, blocName, log); inst != nil {
		return inst, nil
	}

	// Fall back to name-based discovery
	if inst := tryNameBasedInstanceDiscovery(ctx, provider, blocName, log); inst != nil {
		return inst, nil
	}

	return nil, ErrNoBastionHostFound(blocName)
}

func tryLabelBasedInstanceDiscovery(ctx context.Context, provider cpi.Provider, blocName string, log logger.Logger) *cpi.Instance {
	labelKeys := []string{"component", "role", "job"}
	for _, key := range labelKeys {
		filters := map[string]string{
			"label.bloc":   blocName,
			"label." + key: RoleBastion,
		}

		log.Debugf("Label instance discovery: trying key=%s filters=%v", key, filters)

		instances, err := provider.ComputeManager().ListInstances(ctx, filters)
		if err != nil {
			log.Debugf("Label instance discovery: key=%s failed: %v", key, err)

			continue
		}

		if len(instances) == 0 {
			log.Debugf("Label instance discovery: key=%s returned 0 instances", key)

			continue
		}

		log.Debugf("Label instance discovery: key=%s found %d instances", key, len(instances))

		return instances[0]
	}

	return nil
}

func tryNameBasedInstanceDiscovery(ctx context.Context, provider cpi.Provider, blocName string, log logger.Logger) *cpi.Instance {
	// Try the tagged path first — providers that honour label filters return
	// the right VM directly. Then fall back to an unfiltered listing so
	// untagged legacy VMs (e.g. PVE bastions created before native tag
	// support landed) are still discoverable by name match against
	// "<bloc>-bastion".
	listAttempts := []map[string]string{
		{"label.bloc": blocName},
		nil,
	}

	expectedName := blocName + "-" + RoleBastion

	for _, filters := range listAttempts {
		log.Debugf("Name instance discovery: listing instances filters=%v", filters)

		instances, err := provider.ComputeManager().ListInstances(ctx, filters)
		if err != nil {
			log.Debugf("Name instance discovery: list with filters=%v failed: %v", filters, err)

			continue
		}

		if len(instances) == 0 {
			log.Debugf("Name instance discovery: no instances returned for filters=%v", filters)

			continue
		}

		log.Debugf("Name instance discovery: checking %d instances for bastion name pattern", len(instances))

		// Prefer exact "<bloc>-bastion" matches before falling back to any
		// instance whose name merely contains "bastion" — important on
		// shared PVE clusters where multiple blocs' bastions coexist.
		for _, inst := range instances {
			if strings.EqualFold(inst.Name, expectedName) {
				log.Debugf("Name instance discovery: matched %s by exact name", inst.Name)

				return inst
			}
		}

		for _, inst := range instances {
			if isBastionInstance(inst.Name) {
				log.Debugf("Name instance discovery: matched %s by name pattern", inst.Name)

				return inst
			}
		}
	}

	return nil
}

// isBastionReachable probes TCP port 22 to validate a candidate bastion
// address before trusting a cached value. Returns true only when a TCP
// handshake completes within bastionReachabilityProbeTimeout. Used to
// guard against the bootstrap-time IP drifting from the running guest's
// actual address (PVE template + predictable interface names → DHCP
// fallback inside the VM).
func isBastionReachable(ctx context.Context, ipAddr string) bool {
	if ipAddr == "" {
		return false
	}

	dialer := net.Dialer{Timeout: bastionReachabilityProbeTimeout}

	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ipAddr, "22"))
	if err != nil {
		return false
	}

	_ = conn.Close()

	return true
}

// tryReservedBastionIP reads the reserved bastion address that bootstrap
// records when laying out the primary OCFP subnet. The output key follows
// the pattern reserved_<subnet>_bastion_ip and the subnet name itself
// follows <bloc>-ocfp-0. The historic state files may also list the
// address under reserved_<bloc>_bastion_ip, so both keys are tried.
//
// Returns the IP address as a string, or "" when neither key is present.
func tryReservedBastionIP(blocName string, log logger.Logger) string {
	stateDir, err := state.GetStateDir(blocName)
	if err != nil {
		log.Debugf("Reserved-IP fallback: state dir lookup failed for %s: %v", blocName, err)

		return ""
	}

	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		log.Debugf("Reserved-IP fallback: state manager init failed for %s: %v", stateDir, err)

		return ""
	}

	_, err = stateManager.Load(blocName)
	if err != nil {
		log.Debugf("Reserved-IP fallback: state load failed for %s: %v", blocName, err)

		return ""
	}

	candidates := []string{
		"reserved_" + blocName + "-ocfp-0_bastion_ip",
		"reserved_" + blocName + "_bastion_ip",
	}

	for _, key := range candidates {
		raw, getErr := stateManager.GetOutput(key)
		if getErr != nil {
			log.Debugf("Reserved-IP fallback: %s not present: %v", key, getErr)

			continue
		}

		ipAddr, ok := raw.(string)
		if !ok || ipAddr == "" {
			log.Debugf("Reserved-IP fallback: %s empty or non-string", key)

			continue
		}

		log.Debugf("Reserved-IP fallback: matched %s=%s", key, ipAddr)

		return ipAddr
	}

	return ""
}

// configBastionIP returns the bloc config's explicit bastion_ip override, or
// "" when no config is loadable or the field is unset. This is the operator's
// reachable entry point (e.g. a tailscale IP) for providers whose discovered
// address is not routable from the operator host.
func configBastionIP(blocName string, log logger.Logger) string {
	cfg, err := config.LoadWithParams(viper.GetString("config"), blocName)
	if err != nil {
		log.Debugf("config bastion_ip: load failed for %s: %v", blocName, err)

		return ""
	}

	return strings.TrimSpace(cfg.BastionIP)
}

func tryStateCache(blocName string, log logger.Logger) (string, bool) {
	// Get standard state directory for this bloc
	stateDir, err := state.GetStateDir(blocName)
	if err != nil {
		log.Debugf("State cache: failed to get state dir for %s: %v", blocName, err)

		return "", false
	}

	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		log.Debugf("State cache: failed to create state manager for %s: %v", stateDir, err)

		return "", false
	}

	_, err = stateManager.Load(blocName)
	if err != nil {
		log.Debugf("State cache: failed to load state for %s: %v", blocName, err)

		return "", false
	}

	// Resolution order:
	//   1. bastion_ssh_host  — set by bootstrap as the canonical SSH target
	//      (public IP when available, else private/Tailscale IP for PVE-style
	//      bridge deployments).
	//   2. bastion_public_ip — historical key used by AWS/STACKIT paths and
	//      legacy state files.
	//   3. bastion_private_ip — last-resort fallback for state files written
	//      before bastion_ssh_host existed and which only have the private
	//      address recorded (e.g. PVE bootstraps prior to the SSH-host fix).
	for _, key := range []string{"bastion_ssh_host", "bastion_public_ip", "bastion_private_ip"} {
		outputVal, getErr := stateManager.GetOutput(key)
		if getErr != nil {
			log.Debugf("State cache: %s not found in state: %v", key, getErr)

			continue
		}

		bastionIP, ok := outputVal.(string)
		if !ok || bastionIP == "" {
			log.Debugf("State cache: %s is empty or not a string", key)

			continue
		}

		logDiscoveryResult(log, "state-cache", key, "", bastionIP)

		return bastionIP, true
	}

	return "", false
}

func isBastionInstance(name string) bool {
	lowerName := strings.ToLower(name)

	return strings.HasSuffix(lowerName, "-"+RoleBastion) || strings.Contains(lowerName, RoleBastion)
}

func logDiscoveryResult(log logger.Logger, strategy, key, name, ipAddress string) {
	if viper.GetBool("debug_lookup") {
		logDebugDiscovery(log, strategy, key, name, ipAddress)
	} else {
		logNormalDiscovery(log, strategy, key, name, ipAddress)
	}
}

func logDebugDiscovery(log logger.Logger, strategy, key, name, ipAddress string) {
	if key != "" {
		log.Infof("Bastion lookup matched: strategy=%s key=%s name=%s ip=%s", strategy, key, name, ipAddress)
	} else {
		log.Infof("Bastion lookup matched: strategy=%s name=%s ip=%s", strategy, name, ipAddress)
	}
}

func logNormalDiscovery(log logger.Logger, strategy, key, name, ipAddress string) {
	if key != "" {
		log.Debugf("Found bastion by %s %s: %s (%s)", strategy, key, name, ipAddress)
	} else {
		log.Debugf("Found bastion by %s: %s (%s)", strategy, name, ipAddress)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}

	return ""
}

// cacheBastionIP saves the discovered bastion IP in the local state store (best-effort).
func cacheBastionIP(blocName, bastionIP string) {
	if bastionIP == "" {
		return
	}

	// Get standard state directory for this bloc
	stateDir, err := state.GetStateDir(blocName)
	if err != nil {
		return
	}

	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		return
	}

	_, err = stateManager.Load(blocName)
	if err != nil {
		return
	}

	err = stateManager.SetOutput("bastion_public_ip", bastionIP)
	if err != nil {
		return
	}

	_ = stateManager.Save()
}
