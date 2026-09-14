# 1. Network Planning: Carving the Address Space

Every director, every Cloud Foundry cell, and every service VM that follows will live inside addresses we choose right now, before a single VM exists. Networking mistakes are the most expensive kind in this stack, because an IP collision discovered during a CF deploy costs us hours, while ten minutes of planning here costs us nothing. So we begin, deliberately, with a map.

## What we are designing

One bloc needs one contiguous supernet, carved into four equal subnets: an infrastructure subnet for the machines that run the platform, and three workload subnets that become our availability zones. Our worked example uses a `/20` cut into four `/22`s:

| Subnet | CIDR | Role |
|--------|------|------|
| `infra` | `10.108.16.0/22` | Bastion, management director, and artifacts store |
| `ocfp-0` | `10.108.20.0/22` | Workload zone 1 (`z1`) |
| `ocfp-1` | `10.108.24.0/22` | Workload zone 2 (`z2`) |
| `ocfp-2` | `10.108.28.0/22` | Workload zone 3 (`z3`) |

Why three workload zones on what may be a single PVE node? Because the kits think in availability zones, and giving them three real subnets now means the same bloc definition scales to a multi-node cluster later without renumbering. On one node, the zones give us placement semantics; on three nodes, they give us fault isolation. Either way, the addressing never changes.

If we are planning inside a larger lab, we zoom out one level first, giving each tenant a clean `/16`, reserving the first `/24` for the tenant's own management, and placing the OCFP supernet at a predictable offset. The wayne lab follows exactly this pattern, where the member's `/16` is `10.108.0.0/16` and the bloc's `/20` starts at `10.108.16.0`.

## Gateways, and how many we end up with

Here is the part worth pausing on. A Proxmox SDN subnet carries its own gateway address, and we start with exactly one subnet, the whole `/20` at `10.108.16.1`. BOSH validates that a VM's gateway falls inside its subnet's CIDR whenever the subnet is defined narrowly, and `10.108.16.1` falls inside `infra` alone. That is why the management director lives in `infra`. It is created by `bosh create-env` against a strict subnet definition, and `infra` is the only one of the four whose range contains that address.

The workload subnets do not borrow it. Bootstrap creates a real SDN subnet for each `/22` in chapter 4, each with its own gateway at `.1`, so the kits are handed `10.108.20.1`, `10.108.24.1`, and `10.108.28.1` for the three zones. We can see all five subnets afterwards with `pmx pve sdn subnet list ocfp`, and the generated cloud config names the per-zone gateway rather than the supernet's. Keep that in mind whenever a VM in a workload zone cannot reach its gateway, because the address to test is the zone's own `.1` and not `10.108.16.1`.

Each gateway is also that subnet's DNS. The SDN host service runs dnsmasq as a forwarder, so every VM uses its own zone's `.1` for both its default route and its resolver. One packaging wrinkle is that PVE does not ship dnsmasq. Before the zone can serve DHCP or DNS we install it once on the host and disable the distribution's default instance, because the SDN spawns its own per-zone unit (`dnsmasq@<zone>`):

```bash
pmx pve node exec <node> -- apt install -y dnsmasq
pmx pve node exec <node> -- systemctl disable --now dnsmasq
```

The PVE API has no endpoint for installing packages, so `node exec` reaches the node over SSH rather than over the API. We still drive the step from the workstation, which is the point.

Skipping this fails quietly. The SDN applies cleanly, but nothing listens on `10.108.16.1:53`, and every VM boots with a dead resolver.

Its sibling wrinkle is IP forwarding. The SNAT rule the subnet asks for only *translates* packets. The kernel must also be willing to *forward* them, and a stock PVE host ships with `net.ipv4.ip_forward = 0`. Enable it now and persist it:

```bash
pmx pve node exec <node> -- sysctl -w net.ipv4.ip_forward=1
pmx pve node exec <node> -- \
  sh -c "echo 'net.ipv4.ip_forward = 1' > /etc/sysctl.d/99-ocfp-sdn-forward.conf"
```

The redirection has to run on the node, which is why the second call wraps it in `sh -c`. Without that wrapper, the shell in front of us would create the file on our own workstation, and the node would stay unchanged.

This failure is even quieter than the dnsmasq one. DHCP works (dnsmasq is host-local), DNS works, and every packet bound for the internet dies at the host. The first symptom is an `apt-get update` inside a template seed VM fetching nothing, and `apt-get update` exits zero even when all its mirrors are unreachable, so the error surfaces one step later as `Unable to locate package`. The bastion is never in the data path, because it is an operator jumpbox we can rebuild at will, and nothing routes through it.

## The SDN zone is Simple, not VXLAN

On a single PVE node we create a **Simple** SDN zone, and this is a hard requirement rather than a taste. A VXLAN zone on one node will happily accept a gateway and SNAT flag in its config, then render neither, leaving no IP on the vnet bridge and no POSTROUTING rule. Every VM loses egress, and the failure looks like a DNS problem. Simple zones (and EVPN, when a second node arrives) are the ones that actually materialize L3 on the host.

Now we create the zone, the vnet, and the subnet from the workstation, against our context. That context is the root context we bootstrap at the top of [chapter 2](02-pve-foundation.md), so flip ahead if pmx knows no context yet. Like the raw API, pmx stages SDN changes, so nothing lands until we apply. The vnet ID must be a bare name of eight alphanumerics or fewer, and ours is simply `ocfp`. The subnet is the full supernet with the gateway and SNAT enabled:

```bash
pmx pve sdn zone create ocfpz --type simple --ipam pve --dhcp dnsmasq
pmx pve sdn vnet create ocfp --zone ocfpz
pmx pve sdn subnet create ocfp 10.108.16.0/20 \
  --gateway 10.108.16.1 --snat \
  --dhcp-range start-address=10.108.16.200,end-address=10.108.16.250 \
  --dhcp-dns-server 10.108.16.1
pmx pve sdn apply
```

The `--dhcp dnsmasq` backend is what actually starts the per-zone dnsmasq unit, and without it there is no DHCP for template seed VMs and no DNS answering on the gateway. The seed range `.200`–`.250` sits deliberately above every static and available band from the plan below.

**Verify**: `pmx pve sdn status zones --node <node>` reports the zone available. Then `pmx pve node exec <node> -- ip addr show ocfp` shows `10.108.16.1/20` on the vnet bridge, and `pmx pve node exec <node> -- iptables -t nat -L POSTROUTING -n | grep 10.108.16` shows the SNAT rule. A test VM attached to `ocfp` can `ping 1.1.1.1` and resolve names through `10.108.16.1`.

**Rollback**: delete in reverse order and re-apply, running `pmx pve sdn subnet delete ocfp 10.108.16.0/20 --yes`, then `pmx pve sdn vnet delete ocfp --yes`, `pmx pve sdn zone delete ocfpz --yes`, and `pmx pve sdn apply`. Nothing depends on them yet, which is the point of doing this first.

Keep the MTU at 1500. A Simple zone adds no encapsulation, so there is no overhead to budget for, and mismatched MTUs are another failure that masquerades as something else.

## Reserving the addresses that matter

Within each subnet we reserve a static band at the bottom, leave a dynamic band for BOSH to allocate from, and keep the rest in hand. The plan below is the one the CLI will later write into Vault as the bloc's network topology, so we are deciding it now, on paper, and chapter 4 automates it.

The infrastructure subnet, `10.108.16.0/22`, holds the hand-managed machines:

| Address | Occupant |
|---------|----------|
| `10.108.16.1` | SDN gateway and DNS (the PVE host) |
| `10.108.16.3` | Bastion |
| `10.108.16.4` | Management BOSH director |
| `10.108.16.11` | Artifacts store (RustFS, S3 API on `:9000`) |

The three workload `/22`s work differently, and this is the part to understand before reading the next two tables. Their reserved-IP map is tiered by config scope, so each one carries two plans laid over each other. The management scope takes the bottom of the range, the environment scope takes a block above it, and the two never overlap.

Here is the management scope. The same offsets repeat in all three zones, and a service lands in whichever zone its kit places it:

| Address | Occupant |
|---------|----------|
| `10.108.20.5`, `10.108.24.5`, `10.108.28.5` | Management Vault (OpenBao), one node per zone |
| `10.108.24.6` | Jumpbox (`z2`) |
| `10.108.20.7` | Concourse (`z1`) |
| `10.108.20.8` | Prometheus (`z1`) |
| `10.108.20.9` | SHIELD (`z1`) |
| `10.108.24.18` | Doomsday (`z2`) |
| `10.108.20.32`–`.63` | Genesis available band, management scope |

The environment scope starts at `.64` in the same subnets:

| Address | Occupant |
|---------|----------|
| `10.108.20.64` | Environment (ocf) BOSH director |
| `10.108.20.65` | Environment Vault |
| `10.108.24.67` | Blacksmith (`z2`) |
| `10.108.20.97` | HAProxy, the Cloud Foundry front door |
| `10.108.20.96`–`10.108.23.254` | Genesis available band, environment scope (CF VMs and compilation) |

Notice the pattern. Bloc infrastructure sits in `infra`, everything the directors deploy sits in the workload zones at one static apiece, and the two config scopes partition each zone between them. HAProxy is the one static that sits deliberately inside the available band it fronts, at the very bottom of it.

One reserved address is worth calling out precisely because no table above shows it. The management scope reserves `.4` in every workload `/22` for a director, the same way the environment scope reserves `.64`, so `10.108.20.4` is a real entry in the map. Nothing occupies it. Our management director is built by `bosh create-env`, which puts it in `infra` at `10.108.16.4` instead, for the gateway reason above. That leaves `10.108.20.4` reserved and empty, and it is the single easiest address on this page to quote by mistake.

Two consequences follow from the tiering, and both have bitten us. A static read out of the wrong scope looks perfectly plausible, since `.4` and `.64` are both real director offsets, so we always name the scope when we quote an address. And because the offsets belong to a named strategy rather than to the bloc, repartitioning the map moves live services, which is why the deployment files read their statics from Vault rather than writing them down. A PVE bloc that names no strategy resolves to `wide`, the one written for `/22` workload subnets, and that is where every offset on this page comes from.

VMIDs follow a similar discipline. 100 through 199 belong to hand-managed VMs, where the bastion and the artifacts store sit, and the CPI allocates upward from 200.

That floor is set by the BOSH kit rather than by the bloc config, which surprises people who go looking for the knob. Chapter 6 explains where it actually lives, when we build the director whose CPI properties carry it.

## How traffic gets in and out

Egress is the SDN's SNAT, which is outbound only and already handled. Ingress deserves a sentence of honesty. There is no PVE-API-managed DNAT, and we do not port-forward into the bloc. Operators reach the network over Tailscale, where the PVE host or a subnet router advertises the supernet. Public traffic reaches Cloud Foundry through a Cloudflare tunnel that originates *outbound* from inside the bloc and lands on HAProxy. We will build that tunnel in chapter 12. For now it is enough to know that our plan requires no inbound firewall holes at all.

## What we walk away with

Before turning the page, we write the plan down wherever our team keeps such facts, including the subnet table, the reserved statics, the gateway, and the domains. Chapter 3 transcribes it into the bloc config, and every later chapter trusts it.

**Verify**: the SDN zone and vnet exist, a test VM on the vnet has egress and DNS through `<gateway>`, and our address plan names a home for the bastion, both directors, the artifacts store, the Vault nodes, HAProxy, and an available band with room for Cloud Foundry plus compilation VMs.

Next, we introduce ourselves to the hypervisor in [2. PVE foundation](02-pve-foundation.md).
