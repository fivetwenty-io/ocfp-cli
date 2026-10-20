package gcp

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

const (
	// ManagedByLabel is the label key indicating OCFP management.
	ManagedByLabel = "managed-by"
	// ManagedByValue is the label value for OCFP-managed resources.
	ManagedByValue = "ocfp"
	// BlocLabel is the label key for bloc name.
	BlocLabel = "bloc"
	// EnvironmentLabel is the label key for environment type.
	EnvironmentLabel = "environment"
	// NameLabel is the label key for resource name.
	NameLabel = "name"
	// JobLabel is the label key for job type (used for public IPs).
	JobLabel = "job"
	// IndexLabel is the label key for index (used for multiple IPs of same type).
	IndexLabel = "index"
	// SecurityGroupLabel is the label key for security group association.
	SecurityGroupLabel = "security-group"

	// DirectionIngress is the firewall rule direction for inbound traffic.
	DirectionIngress = "ingress"

	// AddressStatusInUse indicates a GCP address is currently in use.
	AddressStatusInUse = "IN_USE"

	// OperationStateFailed indicates a GCP operation has failed.
	OperationStateFailed = "FAILED"
)

// labelRegex matches valid GCP label characters.
// GCP labels: lowercase letters, numbers, underscores, hyphens; max 63 chars.
var labelRegex = regexp.MustCompile(`[^a-z0-9_-]`)

// stripLabelPrefix strips the CPI-agnostic "label." or "label:" prefix from a filter key.
// This allows callers to use the CPI-agnostic filter convention (e.g., "label.bloc")
// which gets normalized to the provider-native key (e.g., "bloc") before matching.
func stripLabelPrefix(key string) string {
	switch {
	case strings.HasPrefix(key, "label."):
		return strings.TrimPrefix(key, "label.")
	case strings.HasPrefix(key, "label:"):
		return strings.TrimPrefix(key, "label:")
	default:
		return key
	}
}

// SanitizeLabel sanitizes a string for use as a GCP label value.
// GCP labels must be lowercase, max 63 characters, alphanumeric with underscores and hyphens.
func SanitizeLabel(s string) string { //nolint:varnamelen
	// Convert to lowercase
	s = strings.ToLower(s)

	// Replace invalid characters with hyphens
	s = labelRegex.ReplaceAllString(s, "-")

	// Remove leading/trailing hyphens
	s = strings.Trim(s, "-")

	// Truncate to 63 characters
	if len(s) > 63 { //nolint:mnd
		s = s[:63]
	}

	// Ensure it doesn't end with hyphen after truncation
	s = strings.TrimRight(s, "-")

	return s
}

// BuildLabels creates a standard set of labels for GCP resources.
func BuildLabels(name string, tags map[string]string) map[string]string {
	labels := map[string]string{
		NameLabel:      SanitizeLabel(name),
		ManagedByLabel: ManagedByValue,
	}

	// Add additional tags as labels
	for k, v := range tags {
		labels[SanitizeLabel(k)] = SanitizeLabel(v)
	}

	return labels
}

// BuildLabelsWithBloc creates labels including bloc name.
func BuildLabelsWithBloc(name, bloc string, tags map[string]string) map[string]string {
	labels := BuildLabels(name, tags)
	if bloc != "" {
		labels[BlocLabel] = SanitizeLabel(bloc)
	}

	return labels
}

// MergeLabels merges multiple label maps, with later maps taking precedence.
func MergeLabels(labelMaps ...map[string]string) map[string]string {
	result := make(map[string]string)

	for _, m := range labelMaps {
		for k, v := range m {
			result[k] = v
		}
	}

	return result
}

// MapGCPStateToResourceState maps GCP instance/resource status to CPI ResourceState.
func MapGCPStateToResourceState(gcpState string) cpi.ResourceState {
	switch strings.ToUpper(gcpState) {
	case "PROVISIONING", "STAGING", "PENDING", "CREATING":
		return cpi.ResourceStateCreating
	case "RUNNING", "ACTIVE", "READY":
		return cpi.ResourceStateActive
	case "AVAILABLE":
		return cpi.ResourceStateAvailable
	case "IN_USE", "ATTACHED":
		return cpi.ResourceStateInUse
	case "STOPPING", "SUSPENDING", "SUSPENDED", "STOPPED", "TERMINATED":
		return cpi.ResourceStateStopped
	case "DELETING":
		return cpi.ResourceStateDeleting
	case "DELETED":
		return cpi.ResourceStateDeleted
	case OperationStateFailed, "ERROR":
		return cpi.ResourceStateError
	default:
		return cpi.ResourceStateUnknown
	}
}

// MapDiskStateToResourceState maps GCP disk status to CPI ResourceState.
func MapDiskStateToResourceState(gcpState string) cpi.ResourceState {
	switch strings.ToUpper(gcpState) {
	case "CREATING":
		return cpi.ResourceStateCreating
	case "READY":
		return cpi.ResourceStateAvailable
	case "RESTORING", "DELETING":
		return cpi.ResourceStateDeleting
	case OperationStateFailed:
		return cpi.ResourceStateError
	default:
		return cpi.ResourceStateUnknown
	}
}

// FormatZoneURL formats a zone name to a full resource URL.
func FormatZoneURL(project, zone string) string {
	if strings.Contains(zone, "/") {
		return zone
	}

	return fmt.Sprintf("projects/%s/zones/%s", project, zone)
}

// FormatRegionURL formats a region name to a full resource URL.
func FormatRegionURL(project, region string) string {
	if strings.Contains(region, "/") {
		return region
	}

	return fmt.Sprintf("projects/%s/regions/%s", project, region)
}

// FormatNetworkURL formats a network name to a full resource URL.
func FormatNetworkURL(project, network string) string {
	if strings.Contains(network, "/") {
		return network
	}

	return fmt.Sprintf("projects/%s/global/networks/%s", project, network)
}

// FormatSubnetworkURL formats a subnetwork name to a full resource URL.
func FormatSubnetworkURL(project, region, subnetwork string) string {
	if strings.Contains(subnetwork, "/") {
		return subnetwork
	}

	return fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", project, region, subnetwork)
}

// FormatFirewallURL formats a firewall name to a full resource URL.
func FormatFirewallURL(project, firewall string) string {
	if strings.Contains(firewall, "/") {
		return firewall
	}

	return fmt.Sprintf("projects/%s/global/firewalls/%s", project, firewall)
}

// FormatInstanceURL formats an instance name to a full resource URL.
func FormatInstanceURL(project, zone, instance string) string {
	if strings.Contains(instance, "/") {
		return instance
	}

	return fmt.Sprintf("projects/%s/zones/%s/instances/%s", project, zone, instance)
}

// FormatDiskURL formats a disk name to a full resource URL.
func FormatDiskURL(project, zone, disk string) string {
	if strings.Contains(disk, "/") {
		return disk
	}

	return fmt.Sprintf("projects/%s/zones/%s/disks/%s", project, zone, disk)
}

// FormatImageURL formats an image name to a full resource URL.
func FormatImageURL(project, image string) string {
	if strings.Contains(image, "/") {
		return image
	}

	return fmt.Sprintf("projects/%s/global/images/%s", project, image)
}

// FormatMachineTypeURL formats a machine type name to a full resource URL.
func FormatMachineTypeURL(project, zone, machineType string) string {
	if strings.Contains(machineType, "/") {
		return machineType
	}

	return fmt.Sprintf("projects/%s/zones/%s/machineTypes/%s", project, zone, machineType)
}

// FormatAddressURL formats a regional address name to a full resource URL.
func FormatAddressURL(project, region, address string) string {
	if strings.Contains(address, "/") {
		return address
	}

	return fmt.Sprintf("projects/%s/regions/%s/addresses/%s", project, region, address)
}

// FormatGlobalAddressURL formats a global address name to a full resource URL.
func FormatGlobalAddressURL(project, address string) string {
	if strings.Contains(address, "/") {
		return address
	}

	return fmt.Sprintf("projects/%s/global/addresses/%s", project, address)
}

// FormatRouterURL formats a router name to a full resource URL.
func FormatRouterURL(project, region, router string) string {
	if strings.Contains(router, "/") {
		return router
	}

	return fmt.Sprintf("projects/%s/regions/%s/routers/%s", project, region, router)
}

// ExtractNameFromURL extracts the resource name from a GCP resource URL.
func ExtractNameFromURL(url string) string {
	parts := strings.Split(url, "/")
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}

	return url
}

// ExtractZoneFromURL extracts the zone from a GCP resource URL.
func ExtractZoneFromURL(url string) string {
	parts := strings.Split(url, "/")
	for i, part := range parts {
		if part == "zones" && i+1 < len(parts) {
			return parts[i+1]
		}
	}

	return ""
}

// ExtractRegionFromURL extracts the region from a GCP resource URL.
func ExtractRegionFromURL(url string) string {
	parts := strings.Split(url, "/")
	for i, part := range parts {
		if part == "regions" && i+1 < len(parts) {
			return parts[i+1]
		}
	}

	return ""
}

// ParseTimestamp parses a GCP timestamp string to time.Time.
func ParseTimestamp(timestamp string) time.Time {
	// GCP uses RFC3339 format
	t, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		return time.Time{}
	}

	return t
}

// FormatNetworkTag formats a security group name as a network tag.
// GCP uses network tags to associate instances with firewall rules.
func FormatNetworkTag(securityGroupName string) string {
	return "sg-" + SanitizeLabel(securityGroupName)
}

// FormatFirewallRuleName formats a firewall rule name from security group and rule details.
func FormatFirewallRuleName(securityGroupName, direction string, port int) string {
	name := fmt.Sprintf("sg-%s-%s-%d", SanitizeLabel(securityGroupName), direction, port)
	if len(name) > 63 { //nolint:mnd
		name = name[:63]
	}

	return strings.TrimRight(name, "-")
}

// IsZonalResource checks if a resource type is zone-scoped.
func IsZonalResource(resourceType string) bool {
	zonalResources := map[string]bool{
		"instance":    true,
		"disk":        true,
		"machineType": true,
		"accelerator": true,
	}

	return zonalResources[resourceType]
}

// IsRegionalResource checks if a resource type is region-scoped.
func IsRegionalResource(resourceType string) bool {
	regionalResources := map[string]bool{
		"subnetwork":     true,
		"address":        true,
		"router":         true,
		"forwardingRule": true,
		"backendService": true,
		"healthCheck":    true,
	}

	return regionalResources[resourceType]
}

// IsGlobalResource checks if a resource type is globally-scoped.
func IsGlobalResource(resourceType string) bool {
	globalResources := map[string]bool{
		"network":          true,
		"firewall":         true,
		"route":            true,
		"image":            true,
		"snapshot":         true,
		"sslCertificate":   true,
		"urlMap":           true,
		"targetHttpProxy":  true,
		"targetHttpsProxy": true,
	}

	return globalResources[resourceType]
}

// scopedResource retains zone/project identity across persisted CPI handles.
func scopedResource(project, zone, kind, name string) string {
	return fmt.Sprintf("projects/%s/zones/%s/%s/%s", project, zone, kind, ExtractNameFromURL(name))
}
func resourceScope(project, zone, id string) (string, string, string) {
	parts := strings.Split(strings.TrimPrefix(id, "https://www.googleapis.com/compute/v1/"), "/")
	for i, part := range parts {
		if i+1 < len(parts) {
			if part == "projects" {
				project = parts[i+1]
			}

			if part == "zones" {
				zone = parts[i+1]
			}
		}
	}

	return project, zone, ExtractNameFromURL(id)
}
func resourceIdentity(config *Config, zoneURL, kind, name string) string {
	zone := ExtractZoneFromURL(zoneURL)
	if zone == "" {
		zone = config.Zone
	}

	project, _, _ := resourceScope(config.ProjectID, zone, zoneURL)

	return scopedResource(project, zone, kind, name)
}

type resourceMetadata struct {
	Managed     string            `json:"managed"`
	Description string            `json:"description,omitempty"`
	Tags        map[string]string `json:"tags"`
}

func resourceDescription(description string, tags map[string]string) string {
	b, _ := json.Marshal(resourceMetadata{Managed: "ocfp", Description: description, Tags: tags}) //nolint:errchkjson // Only strings and a string map; encoding cannot fail.

	return string(b)
}
func resourceTags(description string) map[string]string {
	var metadata resourceMetadata
	if json.Unmarshal([]byte(description), &metadata) != nil || metadata.Managed != "ocfp" {
		return nil
	}

	return metadata.Tags
}
func decodedLabels(labels map[string]string) map[string]string {
	result := map[string]string{}
	for key, value := range labels {
		result[key] = value
		if strings.HasPrefix(key, "ocfp-") {
			result["ocfp:"+strings.TrimPrefix(key, "ocfp-")] = value
		}
	}

	return result
}

func isNumericResource(id string) bool {
	name := ExtractNameFromURL(id)
	if name == "" {
		return false
	}

	for _, r := range name {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}

func regionalScope(project, region, id string) (string, string, string) {
	project, _, name := resourceScope(project, "", id)

	parts := strings.Split(id, "/")
	for i, part := range parts {
		if part == "regions" && i+1 < len(parts) {
			region = parts[i+1]
		}
	}

	return project, region, name
}
func regionalIdentity(regionURL, kind, name string) string {
	if regionURL == "" {
		return name
	}

	project, region, _ := regionalScope("", "", regionURL)

	return fmt.Sprintf("projects/%s/regions/%s/%s/%s", project, region, kind, name)
}
