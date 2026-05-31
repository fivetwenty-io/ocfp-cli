# Security Groups

OCFP creates 7 default security groups during bootstrap for every provider. Each group has a specific purpose and a defined set of ingress and egress rules. Groups are named `<bloc>-<group-name>` (e.g., `production-bastion`).

## Default Security Groups

### bastion

Controls SSH access to the bastion host.

| Direction | Protocol | Port | Source | Description |
|-----------|----------|------|--------|-------------|
| ingress | TCP | 22 | `allowed_ingress_ips` (or `0.0.0.0/0` fallback) | SSH |
| egress | all | all | `0.0.0.0/0` | Allow all outbound |

When `allowed_ingress_ips` is configured, a separate ingress rule is created for each CIDR. IPs without a prefix length are normalized to `/32`.

### infra

Controls access to infrastructure services.

| Direction | Protocol | Port | Source | Description |
|-----------|----------|------|--------|-------------|
| ingress | TCP | 22 | any | SSH |
| ingress | TCP | 80 | any | HTTP |
| ingress | TCP | 443 | any | HTTPS |
| ingress | TCP | 8080 | any | HTTP-ALT |
| ingress | TCP | 8443 | any | HTTPS-ALT |
| egress | all | all | `0.0.0.0/0` | Allow all outbound |

### ocfp

Controls access to OCFP platform services.

| Direction | Protocol | Port | Source | Description |
|-----------|----------|------|--------|-------------|
| ingress | TCP | 22 | any | SSH |
| ingress | TCP | 80 | any | HTTP |
| ingress | TCP | 443 | any | HTTPS |
| ingress | TCP | 8443 | any | UAA / HTTPS-ALT |
| ingress | TCP | 8844 | any | CredHub |
| ingress | TCP | 25555 | any | BOSH Director |
| ingress | TCP | 8484 | any | Vault |
| ingress | TCP | 6868 | any | BOSH Agent |
| egress | all | all | `0.0.0.0/0` | Allow all outbound |

### lb-ext

Controls external load balancer access.

| Direction | Protocol | Port | Source | Description |
|-----------|----------|------|--------|-------------|
| ingress | TCP | 443 | `0.0.0.0/0` | HTTPS external |
| egress | all | all | `0.0.0.0/0` | Allow all outbound |

### ocf-cf-router-ingress

Controls Cloud Foundry router ingress traffic.

| Direction | Protocol | Port | Source | Description |
|-----------|----------|------|--------|-------------|
| ingress | TCP | 80 | `0.0.0.0/0` | CF Router HTTP |
| ingress | TCP | 443 | `0.0.0.0/0` | CF Router HTTPS |
| ingress | TCP | 2222 | `0.0.0.0/0` | CF SSH |
| egress | all | all | `0.0.0.0/0` | Allow all outbound |

### ocf-cf-tcp-router-ingress

Controls Cloud Foundry TCP router ingress.

| Direction | Protocol | Port | Source | Description |
|-----------|----------|------|--------|-------------|
| ingress | TCP | 1024-65535 | `0.0.0.0/0` | CF TCP Router |
| egress | all | all | `0.0.0.0/0` | Allow all outbound |

### ocf-cf-ssh-ingress

Controls Cloud Foundry SSH proxy access.

| Direction | Protocol | Port | Source | Description |
|-----------|----------|------|--------|-------------|
| ingress | TCP | 2222 | `0.0.0.0/0` | CF SSH Proxy |
| egress | all | all | `0.0.0.0/0` | Allow all outbound |

## Port Reference

| Port | Protocol | Service |
|------|----------|---------|
| 22 | TCP | SSH |
| 80 | TCP | HTTP |
| 443 | TCP | HTTPS |
| 2222 | TCP | CF SSH / SSH Proxy |
| 6868 | TCP | BOSH Agent |
| 8080 | TCP | HTTP-ALT |
| 8443 | TCP | HTTPS-ALT / UAA |
| 8484 | TCP | Vault |
| 8844 | TCP | CredHub |
| 25555 | TCP | BOSH Director |
| 1024-65535 | TCP | CF TCP Router range |

## Per-Provider Implementation

### AWS

Security groups are implemented as EC2 Security Groups attached to the VPC.

- Groups are created with `CreateSecurityGroup` via the EC2 API
- Each group gets a `Name` tag for console display
- Ingress and egress rules support IPv4 and IPv6 CIDRs
- Rules support security group self-references via `RemoteGroup`
- Rule deduplication prevents duplicate entries

Source: `internal/cpi/aws/security.go`

### Azure

Security groups are implemented as Network Security Groups (NSGs).

- Each NSG is created within the configured resource group
- Rules are priority-based (range 100-4096)
- Direction maps: ingress to Inbound, egress to Outbound
- Protocol handling covers TCP, UDP, ICMP, and wildcard (`*`)
- Port ranges are parsed and validated

Source: `internal/cpi/azure/security.go`

### GCP

Security groups are implemented as VPC firewall rules with network tags.

- Each security group maps to a firewall rule
- Rules use network tags as targets (not instance-level attachment)
- Ingress and egress directions map to separate firewall rule types
- Source ranges (CIDRs) control access
- Port ranges use the GCP `Allowed` format

Source: `internal/cpi/gcp/security.go`

### STACKIT

Security groups use the STACKIT SDK security API.

- Groups are delegated to `m.client.security`
- Standard CRUD operations via the SDK
- Rule management follows the same CPI abstraction

Source: `internal/cpi/stackit/network.go`

### Proxmox

Security groups use the Proxmox VE (PVE) firewall.

- Delegated to `m.client.security`
- Available in both bridge and SDN network modes
- Rules managed through the PVE firewall API

Source: `internal/cpi/pve/network.go`

## Configuration

### Restricting Bastion SSH Access

Configure `allowed_ingress_ips` in your bloc configuration to restrict SSH access to the bastion:

```yaml
allowed_ingress_ips:
  - 203.0.113.10/32
  - 198.51.100.0/24
```

When omitted, bastion SSH is open to `0.0.0.0/0` and a warning is logged.

## Rule Reconciliation

During bootstrap, existing security groups are checked for completeness. If a group exists but is missing rules, OCFP adds the missing rules without removing existing ones. This ensures idempotent bootstrap runs.

The reconciliation flow:

1. Check if group exists in state
2. Verify it exists in the cloud provider
3. List current rules and compare against expected rules
4. Add any missing rules
5. Update state with current timestamp

## See Also

- [Networking Overview](README.md) for the provider support matrix
- [Bastion Initialization](../init/bastion.md) for bastion host setup
- [Per-provider guides](providers/) for provider-specific details
