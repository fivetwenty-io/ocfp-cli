# Load Balancer (lb) Commands

This document describes `ocfp lb` commands, configuration, and how backends are sourced from reserved/public IPs and bloc config.

## Overview

The `lb` command family manages operational load balancers and their backend target pools. For STACKIT, pools often use:
- Reserved IPs computed at bootstrap (from virtual subnets, e.g., vault_ip, doomsday_ip)
- Public IP resources created/listed during bootstrap for jobs like `router`, `tcp-router`, and `cf-ssh`

By default, typed LB commands reconcile pool members from bloc configuration (`lbs:`). If configuration is absent, they fall back to provider conventions (reserved/public IPs).

## Commands

- `ocfp lb ops [--remove-unused] [--with-doomsday]`:
  - Ensures `<bloc>-ops-https` (TCP 443).
  - Default: Reconciles pool from `lbs.ops-https`. Fallback: adds reserved `vault_ip`, `prometheus_ip`, `shield_ip` (ocfp-0); optional `--with-doomsday` adds `doomsday_ip` (ocfp-1).

- `ocfp lb routers [--http] [--https] [--remove-unused]`:
  - Ensures `<bloc>-router-80` (HTTP 80) and/or `<bloc>-router-443` (HTTPS 443).
  - Default: Reconciles from `lbs.router-80` / `lbs.router-443`. Fallback: adds state public IPs where `job=router`.

- `ocfp lb tcp-routers --name <name> --port <port> [--remove-unused]`:
  - Ensures external LB for TCP routers (default name `<bloc>-tcp-router`).
  - Default: Reconciles from `lbs.<name>`. Fallback: adds state public IPs where `job=tcp-router`.

- `ocfp lb cf-ssh [--remove-unused]`:
  - Ensures external LB `<bloc>-cf-ssh` (TCP 2222).
  - Default: Reconciles from `lbs.<name>`. Fallback: adds state public IPs where `job=cf-ssh`.

- `ocfp lb sync --name <key> [--remove-unused]`:
  - Reconciles any LB named `<key>` directly from `lbs.<key>` targets in config.

- `ocfp lb add-service <lb-name> <ip-or-token> [--port <p>] [--target-port <tp>]`:
  - Adds a backend to an LB by raw IP or token (see tokens below).

Other `lb` subcommands (`create`, `delete`, `list`, `status`, `remove-service`, `update`) are available for direct management.

## Target tokens

Two token forms resolve targets without hardcoding IPs:

- `reserved:<key>[:index]` — uses reserved IP outputs created during bootstrap. Examples:
  - `reserved:vault_ip` (from ocfp-0)
  - `reserved:doomsday_ip:1` (from ocfp-1)

- `public-ip:<job>[:index]` — uses public IP resources with matching `job` (and optional `index`) as discovered/ensured by bootstrap. Examples:
  - `public-ip:router:0`
  - `public-ip:tcp-router:1`
  - `public-ip:cf-ssh:0`

Tokens are supported in:
- `lbs:` configuration (Targets list)
- `lb add-service` (for direct additions)

## Bloc configuration (`lbs:`)

Declare desired LBs and their backend targets under the bloc’s `lbs:` key. Typed commands will reconcile LBs to this config by default.

Example:

```yaml
lbs:
  ops-https:
    protocol: tcp
    port: 443
    targets:
      - reserved:vault_ip
      - reserved:prometheus_ip
      - reserved:shield_ip
      - reserved:doomsday_ip:1

  router-80:
    protocol: http
    port: 80
    targets:
      - public-ip:router:0
      - public-ip:router:1

  router-443:
    protocol: https
    port: 443
    targets:
      - public-ip:router:0
      - public-ip:router:1

  cf-ssh:
    protocol: tcp
    port: 2222
    targets:
      - public-ip:cf-ssh:0
```

## Behavior summary

- Config-first: If `lbs.<name>` exists, typed commands reconcile backends to match `Targets` (adds missing; `--remove-unused` prunes extras).
- Conventions fallback:
  - ops: reserved IPs vault/prometheus/shield (ocfp-0) plus optional doomsday (ocfp-1).
  - routers: state public IPs labeled `job=router`.
  - tcp-routers: public IPs labeled `job=tcp-router`.
  - cf-ssh: public IPs labeled `job=cf-ssh`.

## Vault export (STACKIT)

For downstream tools, the Vault writer exports LB service definitions built from the current state:
- Management/ops services using reserved IPs (mirrors Perl): ops-https, prometheus/alertmanager/grafana, shield, doomsday.
- Router services built from public IPs: router-80, router-443, tcp-router, cf-ssh.

Each service entry includes `name`, `protocol`, `port`, and `targets` (list of `{ip, name}`). If `lbs:` config exists and you prefer Vault to strictly reflect config, keep `lbs:` in sync and use `lb sync`/typed commands to enforce it.

## Examples

- Reconcile ops LB from config and prune extras:
  - `ocfp lb ops --remove-unused`

- Ensure routers using fallback public IPs and prune extras:
  - `ocfp lb routers --http --https --remove-unused`

- Add a single backend using tokens:
  - `ocfp lb add-service ops-https reserved:vault_ip`
  - `ocfp lb add-service ops-https public-ip:router:0 --port 443`

## See Also

- [Load Balancer Architecture](../networking/load-balancers.md) for per-provider LB implementation details
- [Public IPs](../networking/public-ips.md) for public IP allocation and labels
- [Networking Overview](../networking/README.md) for the provider support matrix

