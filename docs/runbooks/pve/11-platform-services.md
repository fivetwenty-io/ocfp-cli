# 11. Platform Services: Making It Operable

A platform that runs apps but cannot back itself up, watch itself, or warn us about expiring certificates is a demo. This chapter deploys the services that make the bloc operable, and by now the work has a rhythm. Every one of them is a Genesis kit, addressed the same way, deployed through one of our two directors. The interesting decisions are *which director* and *what each kit needs before it deploys*; the commands themselves will feel like old friends.

## Which zone owns what

The two-tier design from the README becomes concrete here. Services that operate *the platform* belong to the mgmt director; services that live *beside Cloud Foundry* and serve its users belong to the ocf director:

| Service | Zone | Static IP | Role |
|---------|------|-----------|------|
| SHIELD | mgmt | `10.108.20.9` | Backup and restore for Vault, the directors, and service data |
| Prometheus | mgmt | `10.108.20.8` | Metrics and alerting, Grafana on `:3000` |
| Doomsday | mgmt | `10.108.24.9` | Certificate expiry tracker, watching both CredHubs |
| Concourse | mgmt | `10.108.20.7` | CI/CD for the platform team |
| Jumpbox | mgmt | dynamic | Operator shell inside the bloc, with its own user list |
| Blacksmith | ocf | `10.108.20.10` | On-demand data services broker for CF |
| Autoscaler | ocf | dynamic (z2) | App autoscaling for CF |
| Scheduler | ocf | dynamic | Cron-style task scheduling for CF |

The statics are chapter 1's reservations coming due. Each service's address was decided before any of this existed, and the kits read them back out of the Vault topology.

## The mgmt tier

SHIELD first, on the principle that the ability to take backups precedes everything we would want backed up:

```bash
g @ocfp-lab-wayne-mgmt:shield deploy -F -y
g @ocfp-lab-wayne-mgmt:shield do rc --yes
```

That addon is the SHIELD agent runtime config, not core or target registration, whatever its brevity suggests. It installs the runtime-config addon that puts a SHIELD agent on every VM the directors deploy, and it prompts before writing, so pass `--yes` when we mean it.

Prometheus next, so the rest of the tier comes up already watched:

```bash
g @ocfp-lab-wayne-mgmt:prometheus deploy -F -y
```

Doomsday and Concourse both authenticate to Vault through AppRole, and their kits ship addons that provision the role the deploy is about to need, so we have to run them in this order:

```bash
g @ocfp-lab-wayne-mgmt:doomsday do setup-approle
g @ocfp-lab-wayne-mgmt:doomsday deploy -F -y

g @ocfp-lab-wayne-mgmt:concourse do setup-approle
g @ocfp-lab-wayne-mgmt:concourse deploy -F -y
```

The jumpbox closes out the tier. It is a plain deploy, and it reads its user list from `secret/config/<bloc>/mgmt/jumpbox/users`:

```bash
g @ocfp-lab-wayne-mgmt:jumpbox deploy -F -y
```

**Verify** each service from the bastion. SHIELD's API answers on `https://10.108.20.9`, and Prometheus's nginx answers on `10.108.20.8` behind its auth gate with Grafana on `:3000`. Doomsday's API should list both the mgmt and the ocf CredHub as watched backends, and Concourse's ATC should answer on `https://10.108.20.7`. On the mgmt director, `bosh deployments` now reads like the roster of a real platform.

## The ocf tier

Same pattern, other director. Blacksmith is the service broker that gives CF marketplaces their databases. Its forges deploy as part of the kit, but the deploy does not put anything in the marketplace on its own. Registering the broker with the cloud controller is a second, explicit step:

```bash
g @ocfp-lab-wayne-ocf:blacksmith deploy -F -y
g @ocfp-lab-wayne-ocf:blacksmith do register
```

The addon takes an optional CF environment name, and with none given it uses the current environment with a `/cf` suffix. Our Blacksmith and our CF share the environment `ocfp-lab-wayne-ocf`, so the default resolves to the right deployment and we can leave the argument off. Name it explicitly when the two live in different environments. Alongside the registration it synchronizes CA certificates and enables service access for every forge, which is why a marketplace that stays empty usually means this step was skipped rather than that a forge failed.

Autoscaler and Scheduler round out the CF-facing set. Autoscaler needs the same treatment, because its deploy does not bind its broker either:

```bash
g @ocfp-lab-wayne-ocf:autoscaler deploy -F -y
g @ocfp-lab-wayne-ocf:autoscaler do bind-autoscaler

g @ocfp-lab-wayne-ocf:scheduler deploy -F -y
```

Skipping either registration is the most common way this chapter appears to succeed and has not. Every deploy goes green, and the verify below comes back empty, because a broker that nobody told the cloud controller about is a running VM with no way to reach it.

**Verify** that `cf marketplace` grows Blacksmith's service offerings and that `cf service-brokers` lists both Blacksmith and the autoscaler. The full-circle test is a `cf create-service` against a Blacksmith plan, where the broker asks the ocf director to deploy a service instance. That one command is the whole Blacksmith idea.

## The PVE findings, already folded in

Running this many kits through one SDN surfaced three sharp edges during validation. All three are worth knowing, even though the fixes now live in the kits and env files rather than in our hands:

- **`kit.scale` is mandatory.** Without it, OCFP kits recurse into an OOM during scaling lookups. That was chapter 8's rule, and every service env file sets `scale: dev` (or `prod`) for the same reason.

- **Shared bridge, disjoint bands.** Both tiers' deployments allocate dynamic IPs on the same `ocfp` bridge, and two directors will happily hand out the same address unless their available bands are disjoint. The Vault topology gives each tier its own band, and an overlap shows up as an IP-conflict deploy failure. During validation that meant a Concourse worker squatting on HAProxy's static.

- **Dotted network names versus bosh-dns.** Genesis network names contain dots (`ocfp-lab-wayne-ocf.cf.net-ocf`), which breaks bosh-dns long-form link addresses in multi-VM deployments. Kits with interlinked jobs (Concourse's web and db, all of Autoscaler) set `use_dns_addresses: false` in their PVE overlays.

**Rollback** works the same way for every service, because each one is an independent BOSH deployment on its own director. We run `g @env:type deploy -F -y` to converge it again, or we delete the deployment through the director to take it away without touching its neighbors. That independence is the payoff of the whole two-director design.

The bloc is now a platform with a memory, a pulse, and a safety net. One chapter remains, and it gives the bloc a public face in [12. Stratos](12-stratos.md).
