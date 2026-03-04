package vault

// MgmtServices defines the known services for the mgmt environment.
// FQDNs will be pre-populated for all these services.
//
//nolint:gochecknoglobals // package-level constant list of management services
var MgmtServices = []string{
	"vault",
	"concourse",
	"prometheus",
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
