# Bastion Tailscale

This document covers joining a freshly provisioned bastion to a Tailscale tailnet at first boot. The workflow targets providers without native public IPs (Proxmox today, similar on-prem providers tomorrow), where the operator needs reliable inbound reachability without depending on the host network.

## Why Tailscale

Bastions on providers without native public IPs traditionally relied on per-user route hacks: VPN profiles, jumphost SSH chains, or hand-maintained routing tables on each operator's laptop. Tailscale replaces those with a single tailnet identity per bastion:

- The bastion gets a stable `100.x.y.z` tailnet IP and a MagicDNS name (`{hostname}.{tailnet}.ts.net`).

- Operators on the same tailnet reach the bastion by name with no extra routing config.

- Tailscale SSH means the bastion does not need to expose port 22 to anything outside the tailnet.

- DNS sync (see [Cloudflare DNS Sync](../networking/dns-cloudflare-sync.md)) can point public records at the tailnet IP, so internal services resolve without changing operator tooling.

## How it is wired

The cloud-init snippet path used by earlier versions is gone — PVE 9.x's storage upload API rejects `content=snippets` outright, so per-VM cloud-init `runcmd` cannot be delivered through the API. The current flow uses **SMBIOS injection** plus a **firstboot service baked into a dedicated bastion template**.

### Image

Bastions clone from `ubuntu-noble-bastion-template` (see `internal/cpi/pve/templates.go`, catalog key `ubuntu-noble-bastion-template`). The first `ocfp bootstrap --bastion` for any bloc on a fresh cluster auto-provisions this template by:

1. Downloading the upstream Ubuntu Noble cloud image (cached across blocs).

2. Booting it with cloud-init credentials known to OCFP.

3. Driving the boot console via the PVE termproxy WebSocket (`internal/cpi/pve/termproxy.go`) to:
   - wait for cloud-init,
   - `apt-get update && apt-get install -y jq qemu-guest-agent`,
   - write `/usr/local/sbin/ocfp-firstboot`, `/usr/local/sbin/ocfp-tailscale-watchdog`, and the matching `ocfp-firstboot.service` / `ocfp-tailscale-watchdog.{service,timer}` units,
   - enable the units and run `cloud-init clean --logs`,
   - shut the VM down.

4. Converting the stopped VM to a template via `qm template`.

Subsequent bastion bootstraps skip the build and clone directly from the template (~30s per bastion).

### Per-VM delivery

`internal/bootstrap/compute.go::bastionTailscaleSpec` builds a `cpi.TailscaleSpec` from the resolved auth key + the bloc's `tailscale:` config. `internal/cpi/pve/compute.go::configureCloudInit` translates that spec into a base64-encoded payload split across three SMBIOS Type 1 string slots and PUTs it on the VM config:

| SMBIOS field | Content | Read by guest with |
|--------------|---------|--------------------|
| `serial` | auth key | `dmidecode -s system-serial-number` |
| `sku` | JSON config blob (hostname, tags, accept_dns, accept_routes, ssh, exit_node, advertise_routes) | `dmidecode -s system-sku-number` |
| `family` | role discriminator `ocfp-bastion` | `dmidecode -s system-family` |

The PVE config gets a line like:

```
smbios1: base64=1,serial=<b64-authkey>,sku=<b64-json>,family=b2NmcC1iYXN0aW9u
```

### Firstboot

On first boot of a cloned bastion, `ocfp-firstboot.service`:

1. Exits 0 immediately unless `dmidecode -s system-family == ocfp-bastion`. (The same units ship in every clone from this template; other roles cloned from it are unaffected.)

2. Reads the auth key from `system-serial-number` and the JSON config from `system-sku-number`.

3. Idempotently installs tailscale via the official install script.

4. Runs `tailscale up --authkey=... --hostname=... --advertise-tags=tag:ocfp-bastion --ssh --accept-dns=false --accept-routes=false --advertise-routes=<vnet-cidr>` (flags assembled from the SMBIOS config).

The full script lives in `internal/cpi/pve/templates_firstboot.go::firstbootScript`.

`--accept-dns=false` keeps `tailscaled` from rewriting `/etc/resolv.conf` to point at MagicDNS (`100.100.100.100`). If the bastion ever loses its tailnet connection (DERP flap, brief NAT outage), MagicDNS becomes unreachable; if it owned `/etc/resolv.conf`, the bastion would lose DNS entirely and could never recover its tailnet join.

`--accept-routes=false` keeps `tailscaled` from pulling other machines' advertised routes (including the bloc's own `/18`) into the bastion's policy routing table 52. If the bastion accepted its own advertised `/18`, return packets for local VMs would be looped back through `tailscale0` instead of the local SDN bridge, breaking egress.

### Watchdog

A second systemd unit, `ocfp-tailscale-watchdog.timer`, fires `ocfp-tailscale-watchdog.service` 2 minutes after boot and then every 5 minutes. The script re-runs `tailscale up` with the SMBIOS-supplied auth key whenever `tailscale status --json` reports `Self.Online == false`. SMBIOS values persist across reboots (they're set in QEMU machine config), so the watchdog needs no on-disk credentials file.

This watchdog exists because of a lab incident on 2026-05-21: a PVE storage-lock storm caused all 7 bastions to drop tailnet within an 18-second window, and none of them re-established. Without the watchdog, the only recovery path was operator console access. The watchdog brings the bastion back within one timer cycle automatically.

### Advertised routes

The `advertise_routes` field in the SMBIOS JSON makes the bastion act as a subnet router for its bloc's private network. Tailnet peers can then reach bloc-internal VMs (director, workload VMs) by IP without sitting on the bloc's network themselves.

The CIDR is derived in `internal/bootstrap/compute.go::deriveBastionAdvertiseRoutes` by masking `req.StaticPrivateIP` to `req.StaticPrivateIPPrefix`. For a bastion at `10.64.64.3/18`, the advertised route is `10.64.64.0/18`. When either field is absent the firstboot script skips `--advertise-routes`.

Routes do not auto-activate. After the bastion joins the tailnet, the tailnet admin must approve each advertised route once in the Tailscale admin (Machines → bastion → Edit route settings). Subsequent boots reuse the approval.

When the auth key is empty, `req.Tailscale` is nil, `smbios1` is not touched, and the firstboot script exits 0 — the bastion boots without Tailscale.

### Ingress forwarding

Blocs configured with `ingress.provider: tailscale` (see
[Ingress Providers](../networking/ingress-providers.md)) add one more piece
to the SMBIOS `sku` blob: an `ingress` field of `{origin_ip, ports}`, where
`origin_ip` is the CF haproxy origin (`cloudflare.origin`) and `ports`
defaults to `[80, 443]`. Firstboot and the watchdog both read this field
and, when present, install an nftables table named `ocfp_ingress`:

```
table ip ocfp_ingress {
  chain prerouting {
    type nat hook prerouting priority dstnat; policy accept;
    iifname "tailscale0" tcp dport { 80, 443 } dnat to <origin_ip>
  }
  chain postrouting {
    type nat hook postrouting priority srcnat; policy accept;
    ip daddr <origin_ip> tcp dport { 80, 443 } masquerade
  }
}
```

The prerouting rule DNATs inbound tailnet traffic on ports 80/443 to the
haproxy origin. The postrouting masquerade rule exists because the origin's
default route points at the SDN gateway, not the bastion — without it, the
origin's reply to a tailnet client would be sent straight to the SDN
gateway carrying the client's real 100.x source address, which the SDN
cannot route back to, and the connection would blackhole. Masquerading
rewrites the source to the bastion's own address so the reply naturally
routes back through it.

nftables state does not survive a reboot. `ocfp-tailscale-watchdog`
reinstalls the `ocfp_ingress` table only when `nft list table ip
ocfp_ingress` comes back empty, so a reboot self-heals within one watchdog
cycle (≤ 5 minutes) without needless churn on a table that's already
present.

## Operator prerequisites

Complete these once per tailnet, before any bastion is provisioned.

### 1. ACL tag

Add `tag:ocfp-bastion` to your tailnet's ACL `tagOwners` so the bastion can advertise the tag at join time. Example (Tailscale admin → Access Controls):

```jsonc
{
  "tagOwners": {
    "tag:ocfp-bastion": ["group:ops"]
  }
}
```

Any group that owns the tag can mint auth keys that grant it. Tighten ACL rules as desired so only `tag:ocfp-bastion` machines can reach (or be reached by) the workloads behind the bastion.

### 2. SSH ACL rule

The bastion runs `tailscale up --ssh`, which starts the tailscaled SSH server. The server only honors connections the tailnet ACL allows, so without an `ssh` block in the ACL, every `tailscale ssh` attempt is rejected. Add (or extend) the `ssh` array:

```jsonc
{
  "ssh": [
    {
      "action": "accept",
      "src":    ["autogroup:member"],
      "dst":    ["tag:ocfp-bastion"],
      "users":  ["ubuntu", "root", "autogroup:nonroot"]
    }
  ]
}
```

- `action`
  `"accept"` skips the per-session Tailscale identity check. Use `"check"` if you want operators to re-auth in a browser on a configurable cadence (default 12h cache).

- `src`
  `autogroup:member` covers every signed-in tailnet member. Narrow to `["group:ops"]` (or any other group you define) if you want to restrict bastion SSH to a subset of users.

- `dst`
  `tag:ocfp-bastion` matches the tag the bastion self-assigns at join via `--advertise-tags=tag:ocfp-bastion`. Keeps the rule scoped to OCFP bastions instead of every machine in the tailnet.

- `users`
  Local usernames on the bastion that a tailnet user is allowed to land as. `ubuntu` matches the default user from `~/.config/ocfp/config.pve.yml`; `autogroup:nonroot` catches any future non-root account; `root` is optional and only useful when an operator needs raw root rather than `sudo`.

Verify after saving with `tailscale ssh ubuntu@<bastion-host>` from a workstation in the tailnet. The session should land directly without a password or key prompt.

### 3. Reusable, pre-approved auth key

Create the auth key under Tailscale admin → Settings → Keys → Generate auth key:

- Reusable
  yes (so the same key works for every bloc's bastion)

- Pre-approved
  yes (so the bastion joins without an admin tap)

- Tags
  `tag:ocfp-bastion`

- Expiration
  pick whatever fits your rotation policy; the bastion's machine key remains valid after the auth key expires

Copy the `tskey-...` value.

### 4. Tailscale config in `~/.config/ocfp/config.yml`

OCFP reads tailscale settings from the config file at two scopes:

- A top-level `tailscale:` block supplies global defaults for every bloc.

- A per-bloc `tailscale:` block under `blocs.<name>.tailscale` overrides global on a field-by-field basis.

Per-bloc fields take precedence. There is no fallback to a hard-coded vault path — operators who relied on the legacy `secret/ocfp/tailscale/auth_key` location must move the value into the config.

The auth key may be supplied as a literal value or as a vault-path indirection. The two are mutually exclusive within a single scope:

```yaml
tailscale:
  # Pick one of the next two. Setting both is a config error.
  auth_key: "tskey-..."                          # literal
  auth_key_vault_path: "secret/ocfp/tailscale:auth_key"  # "path:key" -> vault read at runtime

  tags:
    - "tag:ocfp-bastion"
  accept_dns: false
  accept_routes: false
  ssh: true
  # exit_node: ""           # optional tailnet exit node
  # advertise_routes: ""    # optional CIDR; auto-derived from bastion static IP+prefix when empty
  # hostname: ""            # optional override; defaults to the bastion VM name

blocs:
  example-bloc:
    provider: pve
    # ... usual bloc fields ...
    tailscale:
      # Per-bloc overrides; any field omitted here inherits from the
      # top-level tailscale: block above.
      auth_key: "tskey-bloc-specific-..."
      hostname: "example-bastion"
```

Storing the key in vault stays a supported option — point `auth_key_vault_path` at any `path:key` location:

```bash
safe write secret/team/tailscale auth_key="tskey-..."
```

```yaml
tailscale:
  auth_key_vault_path: "secret/team/tailscale:auth_key"
```

Rotate by overwriting the literal in the config or the value at the vault path; re-running `ocfp init pve` re-renders the cloud-init snippet but does not re-create existing bastion VMs (see the rotation section below).

## Boot-time flow

```mermaid
sequenceDiagram
    actor Operator
    participant ocfp as ocfp bootstrap
    participant Vault
    participant PVE
    participant Template as ubuntu-noble-bastion-template
    participant Bastion
    participant TS as Tailscale control plane

    Operator->>ocfp: ocfp bootstrap --bloc <name> --bastion
    ocfp->>Vault: read tailscale.auth_key_vault_path (or skip if literal)

    Note over ocfp,Template: First run per cluster — auto-provisions the template
    ocfp->>PVE: clone upstream qcow2, boot with seed credentials
    ocfp->>Template: termproxy: apt install jq+qga,<br/>write firstboot+watchdog units, cloud-init clean, shutdown
    ocfp->>PVE: qm template

    Note over ocfp,Bastion: Every bastion clone
    ocfp->>PVE: clone template, PUT smbios1 (base64 auth key + JSON config + ocfp-bastion family)
    PVE->>Bastion: boot
    Bastion->>Bastion: ocfp-firstboot.service reads SMBIOS via dmidecode<br/>→ install tailscale → tailscale up
    Bastion->>TS: tailscale up --authkey=... --advertise-tags=tag:ocfp-bastion --ssh
    TS-->>Bastion: 100.x tailnet IP, MagicDNS name

    Note over Bastion: Every 5 min
    Bastion->>Bastion: watchdog.timer fires; re-up if Self.Online=false

    Operator->>Bastion: tailscale ssh ubuntu@<bastion-host>
```

## Verification

After `ocfp bootstrap --bastion` reports success (template clone + boot is ~30s; first-ever build of the template is ~3min), confirm the join from the operator side.

- Tailscale admin
  The bastion appears in the machine list with its hostname and the `tag:ocfp-bastion` tag.

- From your laptop
  `tailscale status` shows the bastion online. `tailscale ping <bastion-host>` returns a direct or DERP-relayed RTT.

- SSH
  `tailscale ssh ubuntu@<bastion-host>` succeeds without any extra routing.

- On the bastion (via tailscale ssh)
  ```bash
  sudo systemctl status ocfp-firstboot.service     # code=exited, status=0/SUCCESS
  sudo systemctl status ocfp-tailscale-watchdog.timer  # active (waiting)
  sudo dmidecode -s system-family                  # ocfp-bastion
  ```

If the bastion does not appear within five minutes of VM boot, connect via the PVE serial console and check firstboot output:

```bash
sudo journalctl -u ocfp-firstboot.service --no-pager
sudo dmidecode -s system-family       # must report ocfp-bastion or firstboot exits 0 as no-op
sudo dmidecode -s system-serial-number  # must contain the auth key
sudo tailscale status
```

Common causes:

- Empty auth key
  Bootstrap could not resolve a key from `tailscale.auth_key` or `tailscale.auth_key_vault_path` (per-bloc or global). The PVE config will show only `smbios1: uuid=…` (no `serial=`, no `sku=`, no `family=`). Fix the config block and re-bootstrap.

- Bastion was cloned from the wrong image
  `dmidecode -s system-family` returns nothing or a different value. Confirm `bastion.image: ubuntu-noble-bastion-template` in the bloc config.

- Auth key expired or revoked
  `tailscale up` returned a 401. Mint a new key, update the literal or the vault entry referenced by `tailscale.auth_key_vault_path`, then re-bootstrap.

- Tag not in `tagOwners`
  Tailscale rejects `--advertise-tags`. Add the tag to ACL `tagOwners`.

## Re-running and rotation

`ocfp bootstrap --bastion` is idempotent — when the bastion VM already exists it's skipped. To pick up a rotated auth key on an EXISTING bastion the simplest path is to update the SMBIOS payload in place: `ssh root@<pve-host> 'qm set <vmid> --smbios1 base64=1,serial=<b64-newkey>,sku=<b64-json>,family=b2NmcC1iYXN0aW9u' && qm reboot <vmid>`. The next firstboot iteration on the watchdog timer (≤ 5 min) will re-run `tailscale up` with the new key.

To force a fresh join (e.g., recovering a bastion that was removed from the tailnet), destroy + re-create the VM via `ocfp bootstrap --bastion` after wiping the bloc's bastion state file.

The watchdog handles transient tailnet drops automatically: it re-runs `tailscale up` whenever `Self.Online=false`. No operator action needed after a DERP flap, NAT outage, or coordinated VM-side disruption.

## See Also

- [Bastion Initialization](bastion.md) for the rest of the bastion provisioning workflow
- [Proxmox Networking](../networking/providers/pve.md) for the PVE-specific context
- [SDN Subnet Model](../networking/sdn-subnet-model.md) for why PVE bastions need this
- [Ingress Providers](../networking/ingress-providers.md) for the `ingress.provider` config and the full tailscale ingress data path
- [Cloudflare DNS Sync](../networking/dns-cloudflare-sync.md) for pointing wildcard DNS at the tailnet IP
- [Tailscale ACL reference](https://tailscale.com/kb/1018/acls)
- [Tailscale auth keys](https://tailscale.com/kb/1085/auth-keys)
