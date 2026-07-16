# 1. Network Planning — Carving the Address Space

Everything that follows — every director, every Cloud Foundry cell, every
service VM — will live inside addresses we choose right now, before a single
VM exists. Networking mistakes are the most expensive kind in this stack: an
IP collision discovered during a CF deploy costs us hours, while ten minutes
of planning here costs us nothing. So we begin, deliberately, with a map.

## What we are designing

One bloc needs one contiguous supernet, carved into four equal subnets: an
infrastructure subnet for the machines that run the platform, and three
workload subnets that become our availability zones. Our worked example uses
a `/20` cut into four `/22`s:

| Subnet | CIDR | Role |
|--------|------|------|
| `infra` | `10.108.16.0/22` | Bastion, management director, artifacts store |
| `ocfp-0` | `10.108.20.0/22` | Workload zone 1 (`z1`) |
| `ocfp-1` | `10.108.24.0/22` | Workload zone 2 (`z2`) |
| `ocfp-2` | `10.108.28.0/22` | Workload zone 3 (`z3`) |

Why three workload zones on what may be a single PVE node? Because the kits
think in availability zones, and giving them three real subnets now means the
same bloc definition scales to a multi-node cluster later without renumbering.
On one node, the zones give us placement semantics; on three nodes, they give
us fault isolation. Either way, the addressing never changes.

If we are planning inside a larger lab, we zoom out one level first: give
each tenant a clean `/16`, reserve the first `/24` for the tenant's own
management, and place the OCFP supernet at a predictable offset. The wayne
lab follows exactly this pattern — the member's `/16` is `10.108.0.0/16` and
the bloc's `/20` starts at `10.108.16.0`.

## One gateway to rule the four subnets

Here is the part worth pausing on. Proxmox SDN gives us a single gateway IP
on the vnet — ours is `10.108.16.1` — and that address falls inside the
*infra* subnet's range only. BOSH validates that a VM's gateway lies within
its subnet's CIDR when the subnet is defined narrowly, which shapes two
decisions we make now:

- The management director lives in `infra`, because it is created by
  `bosh create-env` with a strict subnet definition, and only `infra`
  contains the gateway.

- The workload subnets are defined to the kits with the shared gateway and
  DNS at `10.108.16.1`, which the SDN routes for all four `/22`s alike.

That one address is also our DNS. The SDN host service runs dnsmasq as a
forwarder, so every VM we create uses `10.108.16.1` for both its default
route and its resolver. The bastion is never in the data path — it is an
operator jumpbox we can rebuild at will, and nothing routes through it.

## The SDN zone — Simple, not VXLAN

On a single PVE node we create a **Simple** SDN zone, and this is a hard
requirement rather than a taste. A VXLAN zone on one node will happily accept
a gateway and SNAT flag in its config, then render neither: no IP on the vnet
bridge, no POSTROUTING rule. Every VM loses egress, and the failure looks like
a DNS problem. Simple zones (and EVPN, when a second
node arrives) are the ones that actually materialize L3 on the host.

We create the zone and vnet in the PVE UI or via `pvesh`. The vnet ID must be
a bare name of eight alphanumerics or fewer — ours is simply `ocfp` — and the
subnet on it is the full supernet with the gateway and SNAT enabled:

```bash
# On the PVE host, as root — or the equivalent clicks under
# Datacenter → SDN → Zones / VNets.
pvesh create /cluster/sdn/zones --type simple --zone ocfpz --ipam pve
pvesh create /cluster/sdn/vnets --vnet ocfp --zone ocfpz
pvesh create /cluster/sdn/vnets/ocfp/subnets \
  --type subnet --subnet 10.108.16.0/20 \
  --gateway 10.108.16.1 --snat 1
pvesh set /cluster/sdn
```

**Verify**: `ip addr show ocfp` on the PVE host shows `10.108.16.1/20` on the
vnet bridge, and `iptables -t nat -L POSTROUTING -n | grep 10.108.16` shows
the SNAT rule. A test VM attached to `ocfp` can `ping 1.1.1.1` and resolve
names through `10.108.16.1`.

**Rollback**: delete the subnet, vnet, and zone in reverse order and re-apply
with `pvesh set /cluster/sdn`. Nothing depends on them yet — that is the
point of doing this first.

Keep the MTU at 1500. A Simple zone adds no encapsulation, so there is no
overhead to budget for, and mismatched MTUs are another failure that
masquerades as something else.

## Reserving the addresses that matter

Within each subnet we reserve a static band at the bottom, leave a dynamic
band for BOSH to allocate from, and keep the rest in hand. The plan below is
the one the CLI will later write into Vault as the bloc's network topology —
we are deciding it now, on paper, so that chapter 4 can automate it.

| Address | Occupant |
|---------|----------|
| `10.108.16.1` | SDN gateway and DNS (the PVE host) |
| `10.108.16.3` | Bastion |
| `10.108.16.4` | Management BOSH director |
| `10.108.16.11` | Artifacts store (RustFS, S3 API on `:9000`) |
| `10.108.20.4` | Environment (ocf) BOSH director |
| `10.108.20.5`, `10.108.24.5`, `10.108.28.5` | Management Vault — one node per zone |
| `10.108.20.7` | Concourse |
| `10.108.20.8` | Prometheus |
| `10.108.20.9` | SHIELD |
| `10.108.20.10` | Blacksmith |
| `10.108.20.13` | HAProxy — the Cloud Foundry front door |
| `10.108.24.9` | Doomsday |
| `10.108.20.12`–`.29` | Genesis available band (CF VMs and compilation) |

Notice the pattern: bloc infrastructure sits in `infra`, while everything the
directors deploy sits in the workload zones, one static apiece. HAProxy's
static sits deliberately inside the available band it fronts. VMIDs follow a
similar discipline — 100 through 199 belong to hand-managed VMs (the bastion
takes 100, artifacts 101), and the CPI allocates upward from 200.

## How traffic gets in and out

Egress is the SDN's SNAT — outbound only, and already handled. Ingress
deserves a sentence of honesty: there is no PVE-API-managed DNAT, and we do
not port-forward into the bloc. Operators reach the network over Tailscale
— the PVE host or a subnet router advertises the supernet. Public traffic
reaches Cloud Foundry through a Cloudflare tunnel that originates *outbound*
from inside the bloc and lands on HAProxy. We will build that tunnel in
chapter 12; for now it is enough to know that our plan requires no inbound
firewall holes at all.

## What we walk away with

Before turning the page, we write the plan down — subnet table, reserved
statics, gateway, domains — wherever our team keeps such facts. Chapter 3
transcribes it into the bloc config, and every later chapter trusts it.

**Verify**: the SDN zone and vnet exist, a test VM on the vnet has egress and
DNS through `<gateway>`, and our address plan names a home for the bastion,
both directors, the artifacts store, the Vault nodes, HAProxy, and an
available band with room for Cloud Foundry plus compilation VMs.

Next, we introduce ourselves to the hypervisor: [2. PVE
foundation](02-pve-foundation.md).
