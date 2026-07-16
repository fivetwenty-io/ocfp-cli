# 11. Platform Services — Making It Operable

A platform that runs apps but cannot back itself up, watch itself, or warn
us about expiring certificates is a demo. This chapter deploys the services
that make the bloc operable, and by now the work has a rhythm. Every one of
them is a Genesis kit, addressed the same way, deployed through one of our
two directors. The interesting decisions are *which director* and *what
each kit needs before it deploys*; the commands themselves will feel like
old friends.

## Which zone owns what

The two-tier design from the README becomes concrete here. Services that
operate *the platform* belong to the mgmt director; services that live
*beside Cloud Foundry* and serve its users belong to the ocf director:

| Service | Zone | Static IP | Role |
|---------|------|-----------|------|
| SHIELD | mgmt | `10.108.20.9` | Backup and restore — Vault, directors, service data |
| Prometheus | mgmt | `10.108.20.8` | Metrics and alerting, Grafana on `:3000` |
| Doomsday | mgmt | `10.108.24.9` | Certificate expiry tracker, watching both CredHubs |
| Concourse | mgmt | `10.108.20.7` | CI/CD for the platform team |
| Blacksmith | ocf | `10.108.20.10` | On-demand data services broker for CF |
| Autoscaler | ocf | dynamic (z2) | App autoscaling for CF |
| Scheduler | ocf | dynamic | Cron-style task scheduling for CF |

The statics are chapter 1's reservations coming due — each service's address
was decided before any of this existed, and the kits read them from the
Vault topology.

## The mgmt tier

SHIELD first, on the principle that the ability to take backups precedes
everything we would want backed up:

```bash
g @ocfp-lab-wayne-mgmt:shield deploy -F -y
g @ocfp-lab-wayne-mgmt:shield do rc      # register cores/targets post-deploy
```

Prometheus next, so the rest of the tier comes up already watched:

```bash
g @ocfp-lab-wayne-mgmt:prometheus deploy -F -y
```

Doomsday and Concourse both authenticate to Vault via AppRole, and their
kits ship addons that provision the role before the deploy needs it — the
ordering matters:

```bash
g @ocfp-lab-wayne-mgmt:doomsday do setup-approle
g @ocfp-lab-wayne-mgmt:doomsday deploy -F -y

g @ocfp-lab-wayne-mgmt:concourse do setup-approle
g @ocfp-lab-wayne-mgmt:concourse deploy -F -y
```

**Verify**, per service, from the bastion: SHIELD's API answers on
`https://10.108.20.9`; Prometheus's nginx answers (auth-gated) on
`10.108.20.8` with Grafana on `:3000`; Doomsday's API lists both the mgmt
and ocf CredHubs as watched backends; Concourse's ATC answers on
`https://10.108.20.7`. On the mgmt director, `bosh deployments` now reads
like the roster of a real platform.

## The ocf tier

Same pattern, other director. Blacksmith is the service broker that gives
CF marketplaces their databases. Its forges deploy as part of the kit, and
after deploy it registers with the cloud controller:

```bash
g @ocfp-lab-wayne-ocf:blacksmith deploy -F -y
```

Autoscaler and Scheduler round out the CF-facing set:

```bash
g @ocfp-lab-wayne-ocf:autoscaler deploy -F -y
g @ocfp-lab-wayne-ocf:scheduler deploy -F -y
```

**Verify**: `cf marketplace` grows Blacksmith's service offerings, and the
autoscaler and scheduler register their broker endpoints. The full-circle
test is a `cf create-service` against a Blacksmith plan — the broker asks
the ocf director to deploy a service instance, which is the whole
Blacksmith idea in one command.

## The PVE findings, already folded in

Running this many kits through one SDN surfaced three sharp edges during
validation. All three are worth knowing, even though the fixes now live in
the kits and env files rather than in our hands:

- **`kit.scale` is mandatory.** OCFP kits recurse into an OOM during scaling
  lookups without it — chapter 8's rule, and every service env file sets
  `scale: dev` (or `prod`) for the same reason.

- **Shared bridge, disjoint bands.** Both tiers' deployments allocate
  dynamic IPs on the same `ocfp` bridge, and two directors will happily
  hand out the same address unless their available bands are disjoint. The
  Vault topology gives each tier its own band; symptoms of overlap are
  IP-conflict deploy failures (validation's example: a Concourse worker
  squatting on HAProxy's static).

- **Dotted network names versus bosh-dns.** Genesis network names contain
  dots (`ocfp-lab-wayne-ocf.cf.net-ocf`), which breaks bosh-dns long-form
  link addresses in multi-VM deployments. Kits with interlinked jobs
  (Concourse's web and db, all of Autoscaler) set
  `use_dns_addresses: false` in their PVE overlays.

**Rollback**, uniformly: each service is an independent BOSH deployment on
its director — `g @env:type deploy -F -y` to converge, or delete the
deployment via the director to remove it without touching its neighbors.
That independence is the payoff of the whole two-director design.

The bloc is now a platform with a memory, a pulse, and a safety net. One
chapter remains: giving it a public face —
[12. Stratos](12-stratos.md).
