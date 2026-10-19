package vault

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/providers"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type cloudSubnet struct {
	config.Subnet

	ID    string
	Index int
}

func cloudStateSubnets(cfg *config.Config, sm *state.Manager) ([]cloudSubnet, error) {
	if sm == nil {
		return nil, nil
	}

	resources, err := sm.GetResourcesByType("subnet")
	if err != nil {
		return nil, err
	}

	var result []cloudSubnet

	for _, r := range resources {
		if r.State == "deleted" {
			continue
		}

		typ, _ := r.Properties["type"].(string)
		if typ == "" {
			typ = DefaultSubnetType
		}

		role, _ := r.Properties["role"].(string)
		if role == "reserved" {
			typ = "reserved"
		}

		cidr, _ := r.Properties["cidr"].(string)
		az, _ := r.Properties["availability_zone"].(string)

		idx := -1
		if value, ok := r.Properties["workload_index"]; ok {
			idx, _ = strconv.Atoi(fmt.Sprint(value))
		}

		if idx < 0 {
			for i, sub := range cfg.Subnets {
				if sub.Name == r.Name {
					idx = i

					break
				}
			}
		}

		if idx < 0 {
			parts := strings.Split(r.Name, "-")
			idx, _ = strconv.Atoi(parts[len(parts)-1])
		}

		result = append(result, cloudSubnet{Subnet: config.Subnet{Name: r.Name, CIDR: cidr, AvailabilityZone: az, Type: typ}, ID: r.ID, Index: idx})
	}

	return result, nil
}

func cloudSubnets(cfg *config.Config, sm *state.Manager) ([]cloudSubnet, error) {
	var result []cloudSubnet

	var err error

	result, err = cloudStateSubnets(cfg, sm)
	if err != nil {
		return nil, err
	}

	if len(result) == 0 {
		subs := cfg.Subnets
		if len(subs) == 0 {
			subs = cfg.Network.Subnets
		}

		for i, sub := range subs {
			if sub.Type == "" {
				sub.Type = DefaultSubnetType
			}

			result = append(result, cloudSubnet{Subnet: sub, Index: i})
		}
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Type == result[j].Type {
			return result[i].Index < result[j].Index
		}

		return result[i].Type < result[j].Type
	})

	seen := map[string]bool{}

	var cidrs []string

	for _, s := range result {
		key := fmt.Sprintf("%s-%d", s.Type, s.Index)
		if seen[key] {
			return nil, fmt.Errorf("ambiguous subnet index %s", key)
		}

		seen[key] = true

		prefix, err := netip.ParsePrefix(s.CIDR)
		if err != nil || !prefix.Addr().Is4() {
			return nil, fmt.Errorf("invalid IPv4 subnet CIDR %q", s.CIDR)
		}

		if s.Type == DefaultSubnetType {
			cidrs = append(cidrs, s.CIDR)
		}
	}

	if err := validateWorkloadSubnetCIDRs(cfg, "cloud vault topology", cidrs); err != nil {
		return nil, err
	}

	return result, nil
}
func cloudDatabases(sm *state.Manager, env string) map[string]map[string]interface{} {
	result := map[string]map[string]interface{}{}
	if sm == nil {
		return result
	}

	resources, err := sm.GetResourcesByType("database")
	if err != nil {
		return result
	}

	for _, r := range resources {
		if r.Properties["environment"] != env {
			continue
		}

		host, _ := r.Properties["hostname"].(string)
		if host == "" {
			continue
		}

		data := map[string]interface{}{}

		for k, v := range r.Properties {
			if k != "environment" {
				data[k] = v
			}
		}

		result[r.Name] = data
	}

	return result
}
func cloudAZs(cfg *config.Config, subnets []cloudSubnet) map[string]config.AvailabilityZone {
	result := map[string]config.AvailabilityZone{}

	for key, az := range cfg.AZs {
		if az.Zone == "" {
			az.Zone = key
		}

		result[key] = az
	}

	for _, sub := range subnets {
		zone := sub.AvailabilityZone
		if zone == "" {
			continue
		}

		if _, ok := result[zone]; ok {
			continue
		}

		found := false

		for _, az := range result {
			if az.Zone == zone {
				found = true
			}
		}

		if !found {
			result[zone] = config.AvailabilityZone{Zone: zone}
		}
	}

	return result
}
func writeCloudAZs(safe SafeInterface, pb *PathBuilder, cfg *config.Config, env string, subnets []cloudSubnet) error {
	azs := cloudAZs(cfg, subnets)

	keys := make([]string, 0, len(azs))
	for k := range azs {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, key := range keys {
		az := azs[key]

		props := az.CloudProperties
		if props == "" {
			prop := "availability_zone"
			if cfg.Provider == "gcp" {
				prop = "zone"
			}

			b, err := json.Marshal(map[string]string{prop: az.Zone})
			if err != nil {
				return err
			}

			props = string(b)
		}

		if !json.Valid([]byte(props)) {
			return fmt.Errorf("invalid cloud properties for AZ %s", key)
		}

		if err := safe.SetMultiple(pb.GetAZPath(env, key), map[string]interface{}{"name": key, "zone": az.Zone, "cloud_properties": props}); err != nil {
			return err
		}
	}

	return nil
}
func cloudAZKey(cfg *config.Config, zone string) string {
	keys := make([]string, 0, len(cfg.AZs))
	for key := range cfg.AZs {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	for _, key := range keys {
		if cfg.AZs[key].Zone == zone {
			return key
		}
	}

	return zone
}

func cloudFQDNRecord(cfg *config.Config, env string) map[string]interface{} {
	base := cfg.DomainName

	var explicit map[string]string

	if cfg.FQDNs != nil {
		base = pveFirstNonEmpty(cfg.FQDNs.Base, base)
		if env == MgmtEnvType {
			explicit = cfg.FQDNs.Mgmt
		} else {
			explicit = cfg.FQDNs.OCF
		}
	}

	if base == "" && len(explicit) == 0 {
		return nil
	}

	return PopulateFQDNsForEnv(env, explicit, base, config.SystemScoped(cfg))
}

func writeCloudSecurityGroups(safe SafeInterface, pb *PathBuilder, sm *state.Manager, bloc, env string, gcp bool) error {
	if sm == nil {
		return nil
	}

	resources, err := sm.GetResourcesByType("security_group")
	if err != nil {
		return err
	}

	for _, r := range resources {
		if r.ID == "" || r.State == "deleted" {
			continue
		}

		name := strings.TrimPrefix(r.Name, bloc+"-")

		data := map[string]interface{}{"id": r.ID, "name": r.Name}
		if gcp {
			data["network_tag"] = r.Name
		}

		if err := safe.SetMultiple(pb.GetSecurityGroupPath(env, name), data); err != nil {
			return err
		}
	}

	return nil
}
func writeCloudLoadBalancers(safe SafeInterface, pb *PathBuilder, sm *state.Manager, env string) error {
	if sm == nil {
		return nil
	}

	resources, err := sm.GetResourcesByType("load_balancer")
	if err != nil {
		return err
	}

	for _, r := range resources {
		if r.Properties["environment"] != env || r.ID == "" {
			continue
		}

		data := map[string]interface{}{"id": r.ID, "name": r.Name}
		for k, v := range r.Properties {
			data[k] = v
		}

		if err := safe.SetMultiple(pb.GetLoadBalancerPath(env, r.Name), data); err != nil {
			return err
		}
	}

	return nil
}

type reservedIPProvider interface {
	ConfigureReservedIPs(reporter providers.ProgressReporter, phase, total int) error
}

func (a *AWSVaultProvider) ConfigureReservedIPs(_ providers.ProgressReporter, _, _ int) error {
	if err := validateCloudState(a.BlocName); err != nil {
		return err
	}

	subs, err := cloudSubnets(a.Config, a.loadStateManager())
	if err != nil {
		return err
	}

	if len(subs) == 0 {
		return errors.New("reserved IPs require bootstrap state or configured workload subnets")
	}

	for _, env := range []string{MgmtEnvType, OCFEnvType} {
		for _, sub := range subs {
			if sub.Type != DefaultSubnetType {
				continue
			}

			if err := a.configureSubnetReservedIPs(sub.CIDR, sub.Type, sub.Index, env); err != nil {
				return err
			}
		}
	}

	return nil
}
func (g *GCPVaultProvider) ConfigureReservedIPs(r providers.ProgressReporter, p, t int) error {
	return g.awsStateReader().ConfigureReservedIPs(r, p, t)
}

func cloudPrivateKey(cfg *config.Config, bloc, name string) (string, error) {
	if material := cfg.Keys[name]; material != "" {
		return material, nil
	}

	dir := pveFirstNonEmpty(cfg.Bastion.SSHKeyDir, config.OcfpSSHKeyDir(bloc))
	for _, file := range []string{"id_ed25519", "id_rsa"} {
		data, err := os.ReadFile(filepath.Join(dir, file)) // #nosec G304 -- operator-selected SSH key directory with fixed key filenames
		if errors.Is(err, os.ErrNotExist) {
			continue
		}

		if err != nil {
			return "", fmt.Errorf("read BOSH SSH private key: %w", err)
		}

		return string(data), nil
	}

	return "", nil
}

func cloudPublicIPAddress(resource *state.Resource) string {
	for _, key := range []string{"ip_address", "address"} {
		if value, ok := resource.Properties[key].(string); ok {
			if addr, err := netip.ParseAddr(value); err == nil {
				return addr.String()
			}
		}
	}

	return ""
}
func cloudPublicIPNameMatches(name, bloc, job string) bool {
	prefix := bloc + "-ocf-cf-" + job + "-"
	if job == "cf-ssh" {
		prefix = bloc + "-ocf-cf-ssh-"
	}

	if !strings.HasPrefix(name, prefix) {
		return false
	}

	_, err := strconv.Atoi(strings.TrimPrefix(name, prefix))

	return err == nil
}

// adjustCloudReservations keeps provider-owned addresses and the AWS bastion
// out of dynamic allocation without shifting any other static service address.
func (a *AWSVaultProvider) adjustCloudReservations(cidr string, subnetNum int, record map[string]any) error {
	if a.Config.Provider != "aws" && a.Config.Provider != "gcp" {
		return nil
	}

	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return err
	}

	last := netip.MustParseAddr(addOffsetToIP(prefix.Masked().Addr().String(), (1<<(32-prefix.Bits()))-2))

	rangeEnd := last.Prev()
	if a.Config.Provider == "aws" {
		rangeEnd, err = a.adjustAWSBastionReservation(cidr, subnetNum, record, prefix, last, rangeEnd)
		if err != nil {
			return err
		}
	}

	if end, ok := record["available_1"].(string); ok && netip.MustParseAddr(end).Compare(rangeEnd) > 0 {
		startValue, ok := record["available_0"].(string)
		if !ok {
			return fmt.Errorf("missing dynamic range start in %s", cidr)
		}

		start, err := netip.ParseAddr(startValue)
		if err != nil {
			return err
		}

		if rangeEnd.Compare(start) < 0 {
			return fmt.Errorf("cloud reservation exhausts dynamic range in %s", cidr)
		}

		record["available_1"] = rangeEnd.String()
		record["reserved_2"] = rangeEnd.Next().String()
		record["reserved_3"] = last.String()
	}

	return nil
}

func (a *AWSVaultProvider) adjustAWSBastionReservation(cidr string, subnetNum int, record map[string]any, prefix netip.Prefix, last, rangeEnd netip.Addr) (netip.Addr, error) {
	bastion := last

	mgmt, err := reservedIPsForSubnet(cidr, MgmtEnvType, subnetNum, a.Config, a.logger)
	if err != nil {
		return netip.Addr{}, err
	}

	if value, ok := mgmt["bastion_ip"].(string); ok {
		ip, err := netip.ParseAddr(value)

		first := netip.MustParseAddr(addOffsetToIP(prefix.Masked().Addr().String(), 4))
		if err == nil && ip.Compare(first) >= 0 {
			bastion = ip
		}
	}

	if observed, ok := a.observedCloudBastionIP(prefix); ok {
		bastion = observed
	}

	first := netip.MustParseAddr(addOffsetToIP(prefix.Masked().Addr().String(), 4))
	if bastion.Compare(first) < 0 || bastion.Compare(last) > 0 {
		return netip.Addr{}, fmt.Errorf("AWS bastion address %s is reserved by provider", bastion)
	}

	for key, value := range record {
		if strings.HasSuffix(key, "_ip") && key != "bastion_ip" && value == bastion.String() {
			return netip.Addr{}, fmt.Errorf("bastion address %s collides with %s", bastion, key)
		}
	}

	if _, exists := record["bastion_ip"]; exists {
		record["bastion_ip"] = bastion.String()
		record["bastion_a"] = bastion.Prev().String()
		record["bastion_b"] = bastion.Next().String()
	}

	if start, ok := record["available_0"].(string); ok && bastion.Compare(netip.MustParseAddr(start)) >= 0 && bastion.Compare(rangeEnd) <= 0 {
		rangeEnd = bastion.Prev()
	}

	return rangeEnd, nil
}

func (a *AWSVaultProvider) observedCloudBastionIP(prefix netip.Prefix) (netip.Addr, bool) {
	sm := a.loadStateManager()
	if sm == nil {
		return netip.Addr{}, false
	}

	value, err := sm.GetOutput("bastion_private_ip")
	if err != nil {
		return netip.Addr{}, false
	}

	ip, err := netip.ParseAddr(fmt.Sprint(value))

	return ip, err == nil && prefix.Contains(ip)
}

// validateCloudState distinguishes absent bootstrap state (valid for explicitly
// configured resources) from corrupt or unreadable state before any export.
func validateCloudState(bloc string) error {
	dir, err := state.GetStateDir(bloc)
	if err != nil {
		return fmt.Errorf("resolve cloud bootstrap state: %w", err)
	}

	sm, err := state.NewManager(dir)
	if err != nil {
		return fmt.Errorf("open cloud bootstrap state: %w", err)
	}

	if _, err = sm.Load(bloc); err != nil {
		return fmt.Errorf("read cloud bootstrap state: %w", err)
	}

	return nil
}

// cloudLocationAndTags exports observed native identities, never guessed cloud zones or tags.
func (g *GCPVaultProvider) cloudLocationAndTags() (string, string, string, error) {
	sm := g.awsStateReader().loadStateManager()

	subs, err := cloudSubnets(g.Config, sm)
	if err != nil {
		return "", "", "", err
	}

	region, zone, err := g.cloudLocation(subs)
	if err != nil {
		return "", "", "", err
	}

	tags := []string{}

	if sm != nil {
		resources, err := sm.GetResourcesByType("security_group")
		if err != nil {
			return "", "", "", err
		}

		for _, r := range resources {
			if r.State != "deleted" && r.ID != "" {
				tags = append(tags, r.Name)
			}
		}
	}

	sort.Strings(tags)

	encoded, err := json.Marshal(tags)
	if err != nil {
		return "", "", "", fmt.Errorf("encode GCP network tags: %w", err)
	}

	return region, zone, string(encoded), nil
}

func cloudResourceName(id string) string {
	parts := strings.Split(strings.TrimRight(id, "/"), "/")

	return parts[len(parts)-1]
}
func (g *GCPVaultProvider) networkName() string {
	if g.Config.Network.Name != "" {
		return g.Config.Network.Name
	}

	return cloudResourceName(g.awsStateReader().getVPCID())
}

func (g *GCPVaultProvider) cloudLocation(subs []cloudSubnet) (string, string, error) {
	zone := ""

	for _, sub := range subs {
		if sub.Type == DefaultSubnetType && sub.AvailabilityZone != "" {
			zone = sub.AvailabilityZone

			break
		}
	}

	if az, ok := g.Config.AZs[zone]; ok && az.Zone != "" {
		zone = az.Zone
	}

	if zone == "" {
		keys := make([]string, 0, len(g.Config.AZs))
		for key := range g.Config.AZs {
			keys = append(keys, key)
		}

		sort.Strings(keys)

		if len(keys) > 0 {
			zone = g.Config.AZs[keys[0]].Zone
			if zone == "" {
				zone = keys[0]
			}
		}
	}

	region := g.Config.Region
	if len(region) > 2 && region[len(region)-2] == '-' {
		if zone == "" {
			zone = region
		}

		region = region[:len(region)-2]
	}

	if zone != "" {
		if len(zone) < 3 || zone[len(zone)-2] != '-' {
			return "", "", fmt.Errorf("invalid GCP physical zone %q", zone)
		}

		actualRegion := zone[:len(zone)-2]
		if region != "" && region != actualRegion {
			return "", "", fmt.Errorf("GCP zone %s is outside configured region %s", zone, region)
		}

		region = actualRegion
	}

	return region, zone, nil
}

// networkProject retains Shared VPC host identity from the observed resource.
func (g *GCPVaultProvider) networkProject() string {
	id := strings.TrimPrefix(g.awsStateReader().getVPCID(), "https://www.googleapis.com/compute/v1/")

	parts := strings.Split(id, "/")
	if len(parts) >= 4 && parts[0] == "projects" && parts[1] != "" {
		return parts[1]
	}

	if g.Config.EnableSharedVPC && g.Config.HostProjectID != "" {
		return g.Config.HostProjectID
	}

	return g.Config.ProjectID
}
