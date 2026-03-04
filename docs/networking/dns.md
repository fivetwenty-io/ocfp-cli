# DNS Configuration

DNS configuration varies by cloud provider. OCFP passes DNS server settings from the bloc configuration to each provider's network creation API using the appropriate mechanism.

## Provider DNS Support

| Provider | Mechanism | Configuration Field | Managed by OCFP |
|----------|-----------|-------------------|------------------|
| AWS | DHCP options set on VPC | `EnableDnsHostnames`, `EnableDnsSupport` | Yes |
| Azure | DhcpOptions on VNet | `DhcpOptions.DNSServers` | Yes |
| GCP | API-level nameservers | Network address family config | Yes |
| STACKIT | Nameservers on IPv4 body | `CreateNetworkIPv4Body.Nameservers` | Yes |
| Proxmox | Not managed | N/A | No |

## AWS

AWS DNS is configured through VPC attributes and DHCP options.

During network creation, OCFP:

1. Creates the VPC with the configured CIDR
2. Enables `EnableDnsHostnames` so instances receive public DNS names
3. Enables `EnableDnsSupport` so the VPC's DNS resolver is active

Custom DNS servers can be specified via DHCP options sets. When `dns_servers` is configured, OCFP passes them during VPC creation.

Source: `internal/cpi/aws/network.go`

## Azure

Azure DNS is configured through DHCP options on the Virtual Network.

When creating a VNet, OCFP sets `DhcpOptions.DNSServers` if DNS servers are provided in the bloc configuration. Azure VNets default to Azure-provided DNS when no custom servers are specified.

Source: `internal/cpi/azure/network.go`

## GCP

GCP DNS configuration is applied at the network level through the API.

Nameservers are set on the network's address family configuration during creation. GCP networks use Google Public DNS by default when no custom servers are specified.

Source: `internal/cpi/gcp/network.go`

## STACKIT

STACKIT DNS is configured through the `CreateNetworkIPv4Body` nameservers field.

During network creation, OCFP passes the configured DNS servers to the STACKIT SDK. These nameservers are applied to the IPv4 configuration of the network.

Source: `internal/cpi/stackit/network.go`

## Proxmox

DNS is not managed by OCFP for Proxmox deployments. DNS configuration must be handled at the host level or through external means.

## Configuration

Specify DNS servers in the bloc network configuration:

```yaml
network:
  network_cidr: 10.4.0.0/20
  dns_servers:
    - 8.8.8.8
    - 8.8.4.4
```

The `dns_servers` field (also accepted as `dns`) is an array of IP addresses passed to the provider during network creation.

## See Also

- [Networking Overview](README.md) for the provider support matrix
- [Per-provider guides](providers/) for provider-specific details
