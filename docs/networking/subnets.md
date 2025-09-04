# Subnet Functionality and Strategies

This document explains how OCFP handles subnets across providers, with special focus on STACKIT, reserved IPs, and strategy options.

## Overview

- Non-STACKIT providers: OCFP creates real provider subnets. If `subnets:` are omitted, OCFP derives two subnets from the bloc CIDR:
  - `mgmt` (public) and `ocf` (private), splitting the bloc network into two equal child subnets.
  - Example: `/20` → two `/21`; `/23` → two `/24`; `/24` → two `/25`.

- STACKIT: single real network only; OCFP uses virtual subnets recorded in state and Vault (no provider subnets created):
  - Default: one virtual subnet `ocfp-0` equal to the bloc CIDR.
  - Strategy `ocfp-triple`: split the bloc network into four equal parts and use the last three as `ocfp-0..2`. For `/20`: `10.4.4.0/22`, `10.4.8.0/22`, `10.4.12.0/22`.

## Configuration

Top-level bloc config fields:

```yaml
# Provider + network
provider: stackit
network:
  network_cidr: 10.4.0.0/20

# STACKIT virtual subnet strategy
subnet_strategy: ocfp-triple  # or omit for single ocfp-0
```

Non-STACKIT (derived subnets if omitted):

```yaml
# If subnets omitted, OCFP creates:
#  - <bloc>-mgmt (public)
#  - <bloc>-ocf (private)
```

## Reserved IPs (STACKIT)

For virtual subnets, OCFP computes reserved IPs used by ops services and LBs. They are persisted to state outputs with keys like:

- `reserved_<bloc>-ocfp-<n>_<key>`

Key assignments (mgmt defaults):

- ocfp-0: `bastion_ip` (.3), `bosh_ip` (.4), `vault_ip` (.5), `jumpbox_ip` (.6), `concourse_ip` (.7), `prometheus_ip` (.8), `shield_ip` (.9), `blacksmith_ip` (.10)
- ocfp-1: `doomsday_ip` (.9)
- ocfp-2: `ocfp_ui_ip` (.9)
- Ranges: `available_a/b` (11–29), `reserved_a/b` (0–10), `reserved_c/d` (30→end)

These outputs enable:

- LB tokens: `reserved:<key>[:index]` (e.g., `reserved:vault_ip`, `reserved:doomsday_ip:1`)
- Vault export of ops services (ops-https, prometheus, shield, doomsday, etc.)

## State properties for virtual subnets

Each virtual subnet resource in state includes:

- `cidr`, `virtual=true`, `type=public`, `parent_cidr`
- `ip_0` (network address), `ip_n` (last usable), `gateway` (parent base + 1)

Outputs are also provided for:

- `subnet_<bloc>-ocfp-<n>_cidr`, `subnet_<bloc>-ocfp-<n>_ip_0`, `subnet_<bloc>-ocfp-<n>_ip_n`, `subnet_<bloc>-ocfp-<n>_gateway`

## Bastion placement

- Non-STACKIT: bastion prefers the `<bloc>-mgmt` subnet; falls back to any bloc subnet (prefer `type=public`).
- STACKIT: no provider subnet used; bastion is created on the network only (no `subnet_id`), with dependency on the virtual `subnet.<bloc>-ocfp-0`.

## Examples

- Single virtual subnet (default):

```yaml
provider: stackit
network:
  network_cidr: 10.4.0.0/20
# subnet_strategy omitted → ocfp-0 = 10.4.0.0/20
```

- Triple virtual subnets:

```yaml
provider: stackit
network:
  network_cidr: 10.4.0.0/20
subnet_strategy: ocfp-triple
# yields ocfp-0/1/2 (10.4.4.0/22, 10.4.8.0/22, 10.4.12.0/22)
```

See also:
- go/docs/cmds/lb.md for LB commands and tokens
- go/docs/public-ips.md for public IP provisioning and labels

