# Adding More Public IPs

This guide walks through increasing the number of CF SSH or jumpbox public IPs in an OCFP deployment.

## Quick Reference

### CF SSH IPs

```yaml
# ~/.ocfp/blocs/<bloc>.yml
cf_ssh_public_ips: 3
```

```bash
ocfp bootstrap
ocfp lb cf-ssh        # or: ocfp lb sync --name cf-ssh
ocfp lb status
```

### Jumpbox IPs

```yaml
# ~/.ocfp/blocs/<bloc>.yml
jumpbox_public_ips: 4
```

```bash
ocfp bootstrap
ocfp lb status
```

## Adding More CF SSH Public IPs

### Step 1: Edit Bloc Configuration

Open your bloc configuration file and set `cf_ssh_public_ips` to the desired count:

```yaml
# ~/.ocfp/blocs/<bloc>.yml
cf_ssh_public_ips: 3   # default is 1
```

### Step 2: Run Bootstrap

Run `ocfp bootstrap` to allocate the new public IPs with your cloud provider. Bootstrap is idempotent — existing IPs are reused, and only missing IPs are created.

```bash
ocfp bootstrap
```

Each new IP is labeled with `job=cf-ssh` and an `index` (0-based) for identification and token resolution.

### Step 3: Update the CF SSH Load Balancer

Add the new IPs as backend targets on the CF SSH load balancer:

```bash
# Reconcile from lbs: config (recommended)
ocfp lb sync --name cf-ssh

# Or update directly
ocfp lb cf-ssh
```

If you manage LBs declaratively, add the new tokens to your bloc config under `lbs:`:

```yaml
lbs:
  cf-ssh:
    targets:
      - public-ip:cf-ssh:0
      - public-ip:cf-ssh:1
      - public-ip:cf-ssh:2
```

### Step 4: SSH Proxy Scaling (Informational)

For OCFP deployments, the SSH proxy is **automatically moved to the router instance group** via provider-specific overlays (`kits/cf/ocfp/{provider}/ssh-proxy.yml`). This places the SSH proxy on the edge network, enabling it to scale with routers. No additional configuration is needed.

The provider overlay:

- Removes the SSH proxy network properties from the scheduler instance group

- Adds the `ssh_proxy` job and network properties to the router instance group

- Updates the `ssh-proxy.service.cf.internal` DNS alias to resolve to routers

For standard (non-OCFP) CF deployments, enable this behavior via the `ssh-proxy-on-routers` kit feature:

```yaml
kit:
  features:
    - ssh-proxy-on-routers
```

### Step 5: Verify

```bash
# Check LB status and backend targets
ocfp lb status

# Verify public IPs were created
# (provider-specific, e.g., for STACKIT)
stackit public-ip list
```

Public IPs are also exported to Vault as service definitions. The CF SSH service entry at `cf-ssh` includes all allocated CF SSH IP targets.

## Adding More Jumpbox Public IPs

### Step 1: Edit Bloc Configuration

Open your bloc configuration file and set `jumpbox_public_ips` to the desired count:

```yaml
# ~/.ocfp/blocs/<bloc>.yml
jumpbox_public_ips: 4   # default is 2
```

### Step 2: Run Bootstrap

```bash
ocfp bootstrap
```

Bootstrap allocates the requested number of public IPs labeled with `job=jumpbox`.

### Step 3: Understand Jumpbox IP Assignment

The jumpbox kit deploys a single instance with one reserved IP from the subnet. This reserved IP is stored in Vault at:

```text
secret/config/<bloc>/<env-type>/net/subnets/<subnet>/reserved-ips:jumpbox_ip
```

The reserved IP corresponds to slot `.6` in the subnet (the `jumpboxIPSlot` constant). Additional public IPs allocated via `jumpbox_public_ips` are for external access targets or LB backends — they do not create additional jumpbox VMs.

### Step 4: Verify

```bash
# Check public IP allocation
ocfp lb status
```

## Reference

### Default IP Counts

| Job | Config Parameter | Default |
|-----|-----------------|---------|
| Router | `router_public_ips` | 4 |
| CF SSH | `cf_ssh_public_ips` | 1 |
| Jumpbox | `jumpbox_public_ips` | 2 |
| TCP Router | `tcp_router_public_ips` | 2 |
| Ops | (not configurable) | 1 |

### Vault Paths

Reserved IPs for each subnet are stored under:

```text
secret/config/<bloc>/<env-type>/net/subnets/<subnet>/reserved-ips
```

Key reserved IP slots within each subnet:

| Slot | Service |
|------|---------|
| .3 | Bastion |
| .4 | BOSH Director |
| .5 | Vault |
| .6 | Jumpbox |
| .7 | Concourse |
| .8 | Prometheus |
| .9 | SHIELD / Doomsday / OCFP UI |
| .10 | Blacksmith / Shout |

### LB Target Token Syntax

Public IPs can be referenced by token in `lbs:` configuration and `lb add-service` commands:

```text
public-ip:<job>[:index]
```

Examples:

- `public-ip:cf-ssh:0` — first CF SSH public IP

- `public-ip:cf-ssh:2` — third CF SSH public IP

- `public-ip:router:0` — first router public IP

- `public-ip:tcp-router:1` — second TCP router public IP

Reserved IPs can also be referenced:

```text
reserved:<key>[:index]
```

Examples:

- `reserved:vault_ip` — Vault reserved IP (from ocfp-0)

- `reserved:jumpbox_ip` — jumpbox reserved IP (from ocfp-0)

### Source Files

| Topic | File |
|-------|------|
| Config struct (IP count fields) | `internal/config/config.go` |
| IP defaults and ensure functions | `internal/cpi/stackit/network.go` |
| Reserved IP slot assignments | `internal/bootstrap/network.go` |
| Jumpbox vault IP lookup | `kits/jumpbox/manifests/ocfp.yml` |
| CF SSH proxy on routers overlay | `kits/cf/ocfp/{provider}/ssh-proxy.yml` |
| CF SSH proxy feature docs | `kits/cf/MANUAL.md` |
| LB target tokens | `docs/cmds/lb.md` |

## See Also

- [Public IPs](public-ips.md) for provider-specific allocation details and the full IP types table

- [LB Commands](../cmds/lb.md) for target tokens and LB management

- [Load Balancers](load-balancers.md) for provider LB architecture
