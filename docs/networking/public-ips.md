# Public IPs

This document describes how OCFP allocates and manages public IPs across cloud providers. Public IPs are used for external access to bastion hosts, jumpboxes, CF routers, TCP routers, and operational services.

## Provider Comparison

| Feature | AWS | Azure | GCP | STACKIT | Proxmox |
|---------|-----|-------|-----|---------|---------|
| IP type | Elastic IP | Static public IP | Static external address | Labeled public IP | Not supported |
| Allocation | `AllocateAddress` (VPC domain) | Standard SKU, static | Regional static address | STACKIT IAAS SDK | N/A |
| Labeling | Tags | Tags | Labels (job/index) | Labels (managed-by, bloc, job, index) | N/A |
| Association | Instance/ENI | NIC/LB frontend | Instance/forwarding rule | Network interface | N/A |
| Preservation on teardown | Default preserved | Default preserved | Default preserved | Default preserved | N/A |

## IP Types and Defaults

OCFP creates public IPs for these jobs:

| Job | Config Parameter | Default Count | Purpose |
|-----|-----------------|---------------|---------|
| Router | `router_public_ips` | 4 | HTTP/HTTPS traffic routing for CF applications |
| CF SSH | `cf_ssh_public_ips` | 1 | SSH access to CF applications |
| Jumpbox | `jumpbox_public_ips` | 2 | Secure access to jumpbox instances |
| TCP Router | `tcp_router_public_ips` | 2 | TCP traffic routing for CF applications |
| Ops | Not configurable | 1 | Operations and management access |

## Adding More IPs

IP counts are configurable via bloc config parameters (`cf_ssh_public_ips`, `jumpbox_public_ips`, `router_public_ips`, `tcp_router_public_ips`). The workflow for adding more IPs is:

1. Edit your bloc configuration file (`~/.config/ocfp/<bloc>.yml`) to set the desired count

2. Run `ocfp bootstrap` to allocate the new IPs

3. Update the relevant load balancer (`ocfp lb sync` or typed LB commands)

For a full step-by-step walkthrough, including CF kit feature considerations and vault path references, see [Adding More Public IPs](adding-ips.md).

## AWS

AWS public IPs are Elastic IPs allocated in the VPC domain.

### Allocation

- Elastic IPs are allocated via `AllocateAddress` with `Domain: vpc`
- Each IP is tagged with `managed-by=ocfp`, `bloc`, `env=mgmt`, `job`, and `index`
- IPs can be associated with instances or ENIs
- Released via `ReleaseAddress`

### Configuration

```yaml
provider: aws
region: us-east-1
jumpbox_public_ips: 3
router_public_ips: 6
```

Source: `internal/cpi/aws/network.go`

## Azure

Azure public IPs use Standard SKU with static allocation.

### Allocation

- Public IPs are created as `PublicIPAddress` resources with Standard SKU
- Regional tier allocation
- Tagged with `managed-by=ocfp`, `bloc`, `job`, `index`
- Can be associated with NICs or LB frontend configurations

Source: `internal/cpi/azure/network.go`

## GCP

GCP public IPs are static external addresses.

### Allocation

- Addresses are allocated as regional static external IPs
- Labeled with `job` and `index` for identification
- Can be associated with instances or forwarding rules
- Managed through the Compute API addresses resource

Source: `internal/cpi/gcp/network.go`

## STACKIT

STACKIT has the most mature public IP implementation in OCFP.

### Allocation

- Public IPs are created through the STACKIT IAAS SDK
- Each IP is labeled with:
  - `managed-by=ocfp`
  - `bloc=<bloc-name>`
  - `env=mgmt`
  - `job=<job-type>`
  - `index=<0-based-index>`
- Labels are sanitized to match STACKIT's regex: `^(-|_|[a-z0-9]){0,63}$`
- If IPs with matching labels already exist, they are reused

### Ensure Helpers

STACKIT provides specialized ensure functions for each job type:

- `EnsureJumpboxPublicIPs` — creates/ensures jumpbox IPs
- `EnsureOpsPublicIPs` — creates/ensures the ops IP
- `EnsureRouterPublicIPs` — creates/ensures router IPs
- `EnsureCFSSHPublicIPs` — creates/ensures CF SSH IPs
- `EnsureTCPRouterPublicIPs` — creates/ensures TCP router IPs

### Configuration

```yaml
provider: stackit
region: eu01
jumpbox_public_ips: 3
router_public_ips: 6
cf_ssh_public_ips: 2
tcp_router_public_ips: 3
```

### Viewing IPs

```bash
stackit public-ip list
```

Source: `internal/cpi/stackit/network.go`

## Proxmox

Public IPs are not supported for Proxmox. All public IP and floating IP operations return `ErrFloatingIPsNotSupported`.

Source: `internal/cpi/pve/network.go`

## Public IP Tokens

Public IPs can be referenced in LB configurations and `lb add-service` commands using token form:

```text
public-ip:<job>[:index]
```

Examples:

- `public-ip:router:0` — first CF router public IP

- `public-ip:tcp-router:1` — second TCP router public IP

- `public-ip:cf-ssh:0` — first CF SSH public IP

Tokens resolve against state resources of type `public_ip`, matching `job` and optional `index`.

## Public IP Preservation

By default, `ocfp teardown` preserves public IPs to avoid disrupting production traffic. To include public IPs in teardown, use the `--public-ips` flag:

```bash
ocfp teardown --bloc production --region eu01 --public-ips --force
```

## Vault Export

For downstream tools, the Vault writer exports service definitions built from public IPs:

- Router services: `router-80`, `router-443` (from `job=router` IPs)
- TCP router: `tcp-router` (from `job=tcp-router` IPs)
- CF SSH: `cf-ssh` (from `job=cf-ssh` IPs)

Each service entry includes `name`, `protocol`, `port`, and `targets` (list of `{ip, name}`).

## See Also

- [Adding More Public IPs](adding-ips.md) for step-by-step guide to increasing IP counts
- [LB Commands](../cmds/lb.md) for target tokens and LB configuration
- [Load Balancers](load-balancers.md) for provider LB architecture
- [Networking Overview](README.md) for the provider support matrix
