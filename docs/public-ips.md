# Public IP Configuration

This document describes how to configure public IPs in OCFP for different cloud providers.

## STACKIT Provider

### Configuration Summary

STACKIT provider supports configuring the following public IP types:

| IP Type | Configuration Parameter | Default | Purpose |
|---------|------------------------|---------|----------|
| Router | `router_public_ips` | 4 | HTTP/HTTPS traffic routing for Cloud Foundry applications |
| CF SSH | `cf_ssh_public_ips` | 1 | SSH access to Cloud Foundry applications |
| Jumpbox | `jumpbox_public_ips` | 2 | Secure access to jumpbox instances |
| TCP Router | `tcp_router_public_ips` | 2 | TCP traffic routing for Cloud Foundry applications |
| Ops | Not configurable | 1 | Operations and management access |

### Jumpbox Public IPs

OCFP creates jumpbox public IPs for secure access to the jumpbox instances.

#### Configuration

Add the following to your bloc configuration:

```yaml
blocs:
  - name: my-bloc
    provider: stackit
    region: eu01
    # Other configuration...
    
    # Configure the number of jumpbox public IPs (default: 2)
    jumpbox_public_ips: 3
```

#### How It Works

- During the bootstrap process, OCFP will create the specified number of jumpbox public IPs
- Each IP is labeled with:
  - `managed-by=ocfp`
  - `bloc={bloc}`
  - `env=mgmt`
  - `job=jumpbox`
  - `index={0-based-index}`
- If jumpbox IPs already exist with the correct labels, they will be reused
- The IPs are created sequentially from index 0 to (jumpbox_public_ips - 1)
- Default is 2 jumpbox IPs for redundancy

#### Example

To create 3 jumpbox public IPs instead of the default 2:

```yaml
blocs:
  - name: production
    provider: stackit
    region: eu01
    jumpbox_public_ips: 3
```

This will ensure that jumpbox public IPs with indices 0-2 are created during bootstrap.

### Router Public IPs

By default, OCFP creates 4 router public IPs for STACKIT deployments. You can customize this number by adding the `router_public_ips` configuration to your OCFP YAML file.

#### Configuration

Add the following to your bloc configuration:

```yaml
blocs:
  - name: my-bloc
    provider: stackit
    region: eu01
    # Other configuration...
    
    # Configure the number of router public IPs (default: 4)
    router_public_ips: 6
```

#### How It Works

- During the bootstrap process, OCFP will create the specified number of router public IPs
- Each IP is labeled with:
  - `managed-by=ocfp`
  - `bloc={bloc}`
  - `env=mgmt`
  - `job=router`
  - `index={0-based-index}`
- If router IPs already exist with the correct labels, they will be reused
- The IPs are created sequentially from index 0 to (router_public_ips - 1)

### CF SSH Public IPs

OCFP can create CF SSH public IPs for handling SSH access to Cloud Foundry applications.

#### Configuration

Add the following to your bloc configuration:

```yaml
blocs:
  - name: my-bloc
    provider: stackit
    region: eu01
    # Other configuration...
    
    # Configure the number of CF SSH public IPs (default: 1)
    cf_ssh_public_ips: 2
```

#### How It Works

- During the bootstrap process, OCFP will create the specified number of CF SSH public IPs
- Each IP is labeled with:
  - `managed-by=ocfp`
  - `bloc={bloc}`
  - `env=mgmt`
  - `job=cf-ssh`
  - `index={0-based-index}`
- If CF SSH IPs already exist with the correct labels, they will be reused
- The IPs are created sequentially from index 0 to (cf_ssh_public_ips - 1)

### TCP Router Public IPs

OCFP can also create TCP router public IPs for handling TCP traffic to Cloud Foundry applications.

#### Configuration

Add the following to your bloc configuration:

```yaml
blocs:
  - name: my-bloc
    provider: stackit
    region: eu01
    # Other configuration...
    
    # Configure the number of TCP router public IPs (default: 2)
    tcp_router_public_ips: 3
```

#### How It Works

- During the bootstrap process, OCFP will create the specified number of TCP router public IPs
- Each IP is labeled with:
  - `managed-by=ocfp`
  - `bloc={bloc}`
  - `env=mgmt`
  - `job=tcp-router`
  - `index={0-based-index}`
- If TCP router IPs already exist with the correct labels, they will be reused
- The IPs are created sequentially from index 0 to (tcp_router_public_ips - 1)

### Ops Public IP

OCFP also creates a single ops public IP automatically. This is not configurable and is always created with:
- `job=ops`
- `index=0`

### Using public IP tokens in LB targets

You can reference discovered/ensured public IPs in `lbs:` configuration or via `lb add-service` using token form:

- `public-ip:<job>[:index]`

Examples:

- `public-ip:router:0` — first CF router public IP
- `public-ip:tcp-router:1` — second TCP router public IP
- `public-ip:cf-ssh:0` — first CF SSH public IP

These tokens resolve against state resources of type `public_ip`, matching `job` and optional `index`. See go/docs/cmds/lb.md for details.

### Public IP Management

#### Viewing Public IPs

After bootstrap, you can view all public IPs using the STACKIT CLI:

```bash
stackit public-ip list
```

#### Public IP Preservation

By default, the `ocfp teardown` command preserves public IPs to avoid disrupting production traffic. To include public IPs in teardown, you must explicitly use the `--public-ips` flag. The provider is derived from the bloc configuration; no `--iaas` flag is needed:

```bash
ocfp teardown --bloc production --region eu01 --public-ips --force
```

## Implementation Status

The Go implementation now supports:
- Configuration parsing for all public IP types
- Data structures for public IP management
- STACKIT provider methods for public IP operations (Create/List/Get/Delete) using stackit-sdk-go (IAAS)
- Ensure helpers for jumpbox, router, cf-ssh, and tcp-router public IPs
- Bootstrap integration to ensure required public IPs exist and persist them to state
- Teardown integration to optionally discover and delete public IPs when `--public-ips` is used

## Other Providers

Router public IP configuration is currently specific to the STACKIT provider. Other providers may handle public IPs differently based on their networking models.
