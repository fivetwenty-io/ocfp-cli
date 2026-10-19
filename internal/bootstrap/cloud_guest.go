package bootstrap

import (
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/guestboot"
	"strings"
)

func (m *Manager) prepareCloudBastion(req *cpi.InstanceRequest) error {
	provider := strings.ToLower(m.options.Provider)
	if provider != "aws" && provider != "gcp" {
		return nil
	}
	// Public access supplies outbound package/bootstrap connectivity. Inbound
	// access remains controlled by the bloc's security group rules.
	public := true
	req.PublicIP = &public
	req.IPForwarding = req.Ingress != nil || req.Tailscale != nil

	data, err := guestboot.Render(provider, req)
	if err != nil {
		return err
	}

	req.UserData = data

	return nil
}

// cloudBastionIP relocates the legacy +3 slot reserved by AWS itself. The
// Vault adapter reserves the final usable address outside its dynamic bands.
func cloudBastionIP(provider, cidr, address string) string {
	if strings.EqualFold(provider, "aws") {
		for offset := range 4 {
			reserved, err := CalculateIPFromCIDR(cidr, offset)
			if err == nil && address == reserved {
				return CIDRLastUsableIP(cidr)
			}
		}
	}

	return address
}
