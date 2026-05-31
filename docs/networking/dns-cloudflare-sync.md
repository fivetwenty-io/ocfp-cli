# Cloudflare DNS Sync

This document covers `scripts/cloudflare-dns-sync`, which keeps wildcard DNS for each bloc pointed at the bastion's tailnet IP. The workflow targets PVE today but generalizes to any deployment where the bastion sits on a Tailscale tailnet and you want public DNS to resolve internal hostnames.

## Use case

For each bloc, you want two records in a single zone:

- `<base>`
  A record. Apex for the bloc's domain (e.g. `lab.example.com`).

- `*.<base>`
  Wildcard A record. Catches everything under the bloc's domain (`api.lab.example.com`, `console.lab.example.com`, etc.).

Both records point at the bastion's tailnet IP (`100.x.y.z`). Anyone on the tailnet resolves these names normally; off-tailnet clients see the records but cannot route to them, which is the desired behavior for a private deployment.

When a bastion is rebuilt or rotates its tailnet IP for any reason, re-running the script repoints DNS without disturbing other records in the zone.

## Prerequisites

Complete these once per workstation that will run the sync.

- Cloudflare API token
  Generate a token under `My Profile → API Tokens → Create Token`. Scope it `Zone:DNS:Edit` on the specific zone you target. Export as `CF_API_TOKEN` before running the script.

- `fqdns.base` per bloc
  Each bloc in `~/.ocfp/config.pve.yml` (or whatever `OCFP_CONFIG` points at) must have a `fqdns.base` value. Example:

  ```yaml
  blocs:
    ocfp-pve-lab:
      fqdns:
        base: lab.example.com
  ```

- Bastion on the tailnet
  The script discovers the bastion's tailnet IP via `tailscale status --json` on the machine running the script. The operator workstation must be joined to the same tailnet, and each bloc's bastion must already be up and joined (see [Bastion Tailscale](../init/bastion-tailscale.md)).

- Local tools
  `uv` (which fetches `pyyaml` + `requests` on first run via PEP 723 inline metadata) and the `tailscale` CLI.

## Running the sync

From the repo root (or anywhere `scripts/cloudflare-dns-sync` is reachable):

```bash
export CF_API_TOKEN=...
scripts/cloudflare-dns-sync
```

The script:

1. Looks up the zone ID for `$CLOUDFLARE_ZONE` (default `fivetwenty.io`).

2. Iterates every `ocfp-pve-*` bloc found in `$OCFP_CONFIG` (default `~/.ocfp/config.pve.yml`).

3. For each bloc, reads `fqdns.base` and discovers the bastion's tailnet IP via `tailscale status --json`, matching on hostname `{bloc}-bastion`.

4. Upserts `<base>` and `*.<base>` A records pointing at that IP (TTL 60, not proxied).

The script is idempotent. Re-running is safe — existing records are updated in place, missing records are created, unchanged records are left alone. Records the script does not own are never touched.

Per-record output:

- `new  <name> -> <ip>` — record was created.

- `upd  <name> -> <ip> (was <old-ip>)` — record existed; content updated.

- `ok   <name> -> <ip> (unchanged)` — record exists and already points at the right IP.

Skips are logged and non-fatal:

- `skip <bloc> - no fqdns.base`
  Bloc has no `fqdns.base`. Add it to config, re-run.

- `skip <bloc> - no tailscale IP for <bloc>-bastion`
  Bastion not yet joined to the tailnet, or hostname mismatch. Verify with `tailscale status` and re-run.

## Customization

The script reads three environment variables.

- `CF_API_TOKEN` (required)
  API token with `Zone:DNS:Edit` on the target zone. Accepts `CLOUDFLARE_API_TOKEN` as a legacy alias.

- `CLOUDFLARE_ZONE` (optional)
  Zone name. Defaults to `fivetwenty.io`. Set to your own zone before first run.

- `OCFP_CONFIG` (optional)
  Path to the merged config. Defaults to `~/.ocfp/config.pve.yml`.

Example targeting a different zone and config:

```bash
CLOUDFLARE_ZONE=lab.example.com \
OCFP_CONFIG=~/work/ocfp/staging.yml \
CF_API_TOKEN=... \
scripts/cloudflare-dns-sync
```

## Manual fallback

If the Cloudflare API is unavailable (or you prefer dashboard-driven changes), the same record set can be entered by hand:

| Record | Type | Value | TTL | Proxy |
|--------|------|-------|-----|-------|
| `<base>` | A | bastion tailnet IP | 60 | off |
| `*.<base>` | A | bastion tailnet IP | 60 | off |

Get each bastion's tailnet IP from `tailscale status` on a joined workstation, or from the Tailscale admin console (Machines → click the bastion → IPs).

## Adapting to other DNS providers

The script is intentionally small (one zone, one provider, two records per bloc) so adapting it is straightforward. The shape of the work is:

- Discover bastion tailnet IPs
  `tailscale status --json` returns a `Peer` map keyed by tailnet machine. Filter by `HostName == "<bloc>-bastion"` and take `TailscaleIPs[0]`.

- Upsert records
  Most DNS providers expose a `GET` for existing records by name plus a `PUT` (update) or `POST` (create). Mirror the script's `upsert_a` helper.

- Keep TTL low
  The script uses TTL 60 so a bastion rebuild propagates within a minute. Adjust for your provider's minimum.

When porting to a non-Cloudflare zone, keep the iteration shape (read blocs from YAML, tailnet lookup per bloc, two records per bloc) and swap out the Cloudflare API calls.

## See Also

- [Bastion Tailscale](../init/bastion-tailscale.md) for the bastion's tailnet join workflow
- [Proxmox Networking](providers/pve.md) for the PVE-specific provider notes
- [SDN Subnet Model](sdn-subnet-model.md) for why PVE bastions live behind Tailscale
- [Cloudflare DNS API reference](https://developers.cloudflare.com/api/operations/dns-records-for-a-zone-list-dns-records)
