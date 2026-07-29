package vault

// MgmtServices defines the known services for the mgmt environment.
// FQDNs will be pre-populated for all these services.
//
// grafana and alertmanager sit alongside prometheus because the prometheus
// kit's ocfp/meta.yml reads all three through the graft vault operator; a key
// this list omits is never written, and the operator fails the deploy rather
// than defaulting. Their absence also made a stale value uncorrectable, since
// SetMultiple merges rather than replaces.
//
//nolint:gochecknoglobals // package-level constant list of management services
var MgmtServices = []string{
	"vault",
	"concourse",
	"prometheus",
	"grafana",
	"alertmanager",
	"shield",
	"bosh",
	"blacksmith",
	"bastion",
	"doomsday",
	"ocfp_ui",
}

// OCFServices defines the known services for the ocf environment.
// FQDNs will be pre-populated for all these services.
//
//nolint:gochecknoglobals // package-level constant list of OCF services
var OCFServices = []string{
	"cf",
	"system",
	"apps",
	"stratos",
	"router",
	"api",
	"uaa",
	"diego",
	"credhub",
	"loggregator",
	"doppler",
	"log-api",
	"shield",
	"vault",
	"concourse",
	"prometheus",
	"bastion",
	"blacksmith",
	"bosh",
	"ocfp_ui",
}

// GetServicesForEnvType returns the list of known services for an environment type.
func GetServicesForEnvType(envType string) []string {
	switch envType {
	case MgmtEnvType:
		return MgmtServices
	case OCFEnvType:
		return OCFServices
	default:
		return nil
	}
}
