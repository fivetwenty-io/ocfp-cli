# Wayne env-BOSH and CF Deploy

Deploys the Wayne env-BOSH director (10.64.64.12) via mgmt-BOSH, then deploys
Cloud Foundry on that env-BOSH director. Both deploys are hard user gates.

---

## Prerequisites

Complete all of these before running any step below:

- [Wayne Bastion Bringup](02-bastion-bringup.md) — bastion at 10.64.64.3 running and tooled
- [mgmt-BOSH Deploy](03-mgmt-bosh-deploy.md) — mgmt-BOSH at 10.64.64.10 healthy
- Vault unlocked on bastion (`safe status` returns `unseal: true`)
- PVE env vars exported in your shell (carried over from the mgmt-BOSH runbook)

Verify the inherited shell environment:

```bash
env | grep PVE_
```

Required vars:

| Variable | Expected value |
|----------|----------------|
| `PVE_HOST` | `sm-0.lab.fivetwenty.io` |
| `PVE_NODE` | `pve` |
| `PVE_TOKEN_ID` | `ocfp-cpi@pve!wayne` |
| `PVE_TOKEN_SECRET` | (captured during CPI account setup) |

Verify mgmt-BOSH is healthy:

```bash
cd ~/w/fivetwenty/studios/ocfp/src/deployments/fivetwenty-ocfp/bosh
genesis bosh ocfp-pve-wayne-mgmt -- env
```

Expected: director version line with `Name: ocfp-pve-wayne-mgmt`, URL `https://10.64.64.10:25555`.

---

## Part 1: env-BOSH Deploy

> **HARD GATE — W5f.** Do not proceed past this section until mgmt-BOSH is
> confirmed healthy and you are ready to commit 30-50 minutes to the deploy.

### Step 1 — Change to the bosh deployments directory

```bash
cd ~/w/fivetwenty/studios/ocfp/src/deployments/fivetwenty-ocfp/bosh
```

All genesis commands in Part 1 run from this directory.

### Step 2 — Vault secrets prep

Generate all env-BOSH secrets that genesis needs before the deploy can proceed:

```bash
genesis secrets add ocfp-pve-wayne-ocf
```

Genesis prompts for each missing secret category (BOSH mbus password, nats
password, director DB password, blobstore agent key, etc.). Accept the
auto-generated values unless you have a reason to set specific values.

Verify secrets landed:

```bash
safe tree /secret/ocfp/wayne/bosh/ocf/
```

Expected: at least `blobstore/`, `db/`, `nats/`, `ssl/` subtrees populated.

### Step 3 — Manifest dry-run

Generate and inspect the manifest before deploying:

```bash
genesis manifest ocfp-pve-wayne-ocf > /tmp/ocf-manifest.yml
```

Verify these values in the generated manifest:

```bash
# env-BOSH static IP must be .12, never .11 (artifacts VM)
grep -A2 'internal_ip' /tmp/ocf-manifest.yml | head -5

# Confirm this director deploys to mgmt-BOSH, not create-env
grep 'director_uuid\|target\|bosh_env' /tmp/ocf-manifest.yml | head -5
```

Key assertions:

| Field | Required value | Reason |
|-------|---------------|--------|
| `networks[0].static_ips[0]` | `10.64.64.12` | .11 is artifacts VM — collision |
| `genesis.bosh_env` resolves to | `10.64.64.10` | mgmt-BOSH is the target director |
| `iaas` | `pve` | env yml sets `kit.iaas: pve` |

If the manifest shows `.11` for the director IP, stop and check
`bosh/ocfp-pve-wayne-ocf.yml` — `params.static_ip` must be `10.64.64.12`.

### Step 4 — Deploy env-BOSH

> **Confirm the manifest dry-run passed before running this command.**

```bash
genesis deploy ocfp-pve-wayne-ocf
```

This deploy differs from the mgmt-BOSH deploy:

- Genesis sends the deployment to mgmt-BOSH at 10.64.64.10 via `genesis.bosh_env`
- BOSH creates the env-BOSH VM via the PVE CPI (same CPI running on mgmt-BOSH)
- No `bosh create-env` this time — this is a standard `bosh deploy`

Typical duration: **30–50 minutes** (longer if stemcell upload or release
compilation is needed on mgmt-BOSH).

Monitor progress via the genesis/bosh output. When complete, genesis writes
exodus data for env-BOSH to vault at `/secret/exodus/ocfp-pve-wayne-ocf/bosh/`.

### Step 5 — Verify env-BOSH

```bash
# Director reachable and healthy
genesis bosh ocfp-pve-wayne-ocf -- env

# No unexpected VMs (only the director itself at this point)
genesis bosh ocfp-pve-wayne-ocf -- vms
```

Expected from `genesis bosh ocfp-pve-wayne-ocf -- env`:

- `Name: ocfp-pve-wayne-ocf`
- `URL: https://10.64.64.12:25555`
- `CPI: pve_cpi`
- Director version line

Expected from `genesis bosh ocfp-pve-wayne-ocf -- vms`: empty deployment list
(env-BOSH has no BOSH deployments yet — CF will be the first).

Run the env-BOSH smoke test:

```bash
~/w/fivetwenty/studios/ocfp/src/clis/ocfp/scripts/smoke/02-env-bosh-smoke.sh
```

Expected: exit 0. The script checks director reachability, API auth, and stemcell
availability.

---

## Part 2: CF Deploy

> **HARD GATE — W5g.** Do not proceed past this section until env-BOSH is
> confirmed healthy (Step 5 passed) and you are ready to commit 1-2 hours to
> the CF deploy.

### Step 6 — CF kit link and cloud-config prep

Switch to the CF deployments directory:

```bash
cd ~/w/fivetwenty/studios/ocfp/src/deployments/fivetwenty-ocfp/cf
```

Link the local CF kit for dev deploys:

```bash
genesis @dev:cf link ~/w/fivetwenty/studios/ocfp/src/kits/cf
```

Verify the Wayne CF cloud-config network override ops file is present. This file
fixes the upstream CF kit's hardcoded `192.168.1.x` reserved range:

```bash
ls ~/w/proxmox/bosh-pve-cpi-release/manifests/wayne/cf-cloud-config-net-override.yml
```

This ops file must exist before the CF deploy. It patches the cloud-config
network block to use `10.64.64.0/18` with the haproxy static IP at `10.64.64.50`.
If it is missing, the ops file was not created during the PVE CPI release prep wave. Create it at `~/w/proxmox/bosh-pve-cpi-release/manifests/wayne/cf-cloud-config-net-override.yml` before continuing.

### Step 7 — Vault secrets prep for CF

CF has significantly more secrets than a BOSH director:

```bash
genesis secrets add ocfp-pve-wayne-cf
```

Secret categories generated include:

| Category | Contents |
|----------|----------|
| NATS | BOSH message bus credentials |
| UAA | Admin client secret, signing keys |
| Cloud Controller | DB password, encryption keys |
| Diego | BBS certs, rep certs, auctioneer certs |
| Loggregator | Doppler keys, traffic controller |
| Router | TLS certs for gorouter |
| HAProxy | Backend health check credentials |
| System domain | Wildcard TLS cert for `*.apps.ocf.wayne.lab.fivetwenty.io` |

Accept auto-generated values. This step takes 1-2 minutes due to RSA key
generation.

Verify:

```bash
safe tree /secret/ocfp/wayne/cf/ | head -30
```

### Step 8 — Manifest dry-run

```bash
genesis manifest ocfp-pve-wayne-cf > /tmp/cf-manifest.yml
```

Verify these critical values:

```bash
# System domain
grep 'system_domain' /tmp/cf-manifest.yml

# HAProxy static IP
grep -A5 'haproxy' /tmp/cf-manifest.yml | grep -i 'ip\|address'

# Cloud config network (must NOT show 192.168.1.x)
genesis bosh ocfp-pve-wayne-cf -- cloud-config | grep -A10 'subnets'
```

Required assertions:

| Field | Required value |
|-------|---------------|
| `system_domain` | `ocf.wayne.lab.fivetwenty.io` |
| haproxy static IP | `10.64.64.50` |
| cloud-config `range` | `10.64.64.0/18` (not `192.168.1.0/24`) |

If `system_domain` is wrong, check `cf/ocfp-pve-wayne-cf.yml` `params.system_domain`.
If the cloud-config still shows `192.168.1.x`, the network override ops file
was not applied — see Step 6 and re-upload the cloud-config manually:

```bash
genesis bosh ocfp-pve-wayne-ocf -- update-cloud-config \
  <(bosh int manifests/cf/cloud-config.yml \
      -o manifests/wayne/cf-cloud-config-net-override.yml)
```

### Step 9 — Deploy CF

> **Confirm the manifest dry-run passed and cloud-config is correct before
> running this command.**

```bash
genesis deploy ocfp-pve-wayne-cf
```

This is the largest deploy in the Wayne bringup sequence:

- **Duration:** 1–2 hours
- **VM count:** 30+ (uaa, cloud_controller, doppler, gorouter, diego_brain,
  diego_cell, nats, api, scheduler, credhub, log_cache, and others)
- Genesis sends the deployment to env-BOSH at 10.64.64.12
- Stemcells and releases are uploaded to env-BOSH on first run

Monitor progress in the genesis/bosh output. Long compilation tasks run in
parallel — cell compilation takes the most time on first deploy.

### Step 10 — Verify CF

```bash
# All VMs running
genesis bosh ocfp-pve-wayne-cf -- vms

# HAProxy reachable on static IP
curl -k https://10.64.64.50/info
```

Expected from `vms`: all 30+ VMs in `running` state. No `failing` or `unresponsive` entries.

Expected from the `curl`: a JSON blob containing `cf_version`, `api_version`, and
`authorization_endpoint`. Example:

```json
{
  "name": "",
  "build": "",
  "support": "https://support.cloudfoundry.org",
  "version": 0,
  "description": "",
  "authorization_endpoint": "https://login.system.ocf.wayne.lab.fivetwenty.io",
  "token_endpoint": "https://uaa.system.ocf.wayne.lab.fivetwenty.io",
  "api_version": "2.189.0",
  ...
}
```

Run the CF smoke test:

```bash
~/w/fivetwenty/studios/ocfp/src/clis/ocfp/scripts/smoke/03-cf-smoke.sh
```

Expected: exit 0. The script pushes a minimal hello-world app to the
`system/smoke` space, verifies routing through HAProxy, and cleans up.

---

## Troubleshooting

### env-BOSH: PVE CPI errors

Same CPI as mgmt-BOSH. Look for:

- `VMID already in use` — another VM at the target VMID; verify VMID range
  starts at 200 (`manifests/wayne/vmid-range.yml`), or check for manually
  created VMs in the 200-299 range on sm-0
- `API auth failed` — token expired; re-export `PVE_TOKEN_SECRET` and update
  the vault path `/secret/ocfp/wayne/pve/cpi_token_secret`
- `storage pool not found` — verify `data` (lvmthin) and `zfs-1` (zfspool) are
  online: `ssh root@sm-0 pvesh get /nodes/pve/storage`

### env-BOSH: stemcell not on env-BOSH

If `genesis deploy ocfp-pve-wayne-cf` fails because stemcell is missing:

```bash
genesis bosh ocfp-pve-wayne-ocf -- upload-stemcell \
  $(genesis bosh ocfp-pve-wayne-mgmt -- stemcells | grep ubuntu | awk '{print $1}')
```

Or let BOSH auto-upload by ensuring the deployment manifest references a
stemcell version available in bosh.io and env-BOSH has internet access via
the bastion NAT configuration.

### CF: gorouter not reachable

If `curl -k https://10.64.64.50/info` times out or returns a connection error:

1. Verify HAProxy is running:
   `genesis bosh ocfp-pve-wayne-cf -- vms | grep haproxy`
2. Check HAProxy backend health:
   `genesis bosh ocfp-pve-wayne-cf -- ssh haproxy/0 -- sudo /var/vcap/jobs/haproxy/bin/drain status`
3. Verify gorouter VMs are `running`:
   `genesis bosh ocfp-pve-wayne-cf -- vms | grep router`

### CF: UAA fails to start

UAA failure on first deploy is almost always a DB credential issue:

```bash
genesis bosh ocfp-pve-wayne-cf -- logs --recent uaa
```

Look for `javax.persistence.PersistenceException` or connection refused to
`10.64.64.12:5432` (internal CF DB). If so:

```bash
# Re-check vault credentials are in place
safe get /secret/ocfp/wayne/cf/uaa/db
safe get /secret/ocfp/wayne/cf/cc/db
```

If either key is missing, re-run `genesis secrets add ocfp-pve-wayne-cf` and
then `genesis deploy ocfp-pve-wayne-cf`.

### CF: diego_cell failures

Cell failures on first deploy are usually a stemcell mismatch between mgmt-BOSH
and env-BOSH. Verify both directors have the same stemcell version:

```bash
genesis bosh ocfp-pve-wayne-mgmt -- stemcells
genesis bosh ocfp-pve-wayne-ocf -- stemcells
```

Versions must match. Upload the matching stemcell to env-BOSH if it is missing.

### CF cloud-config still shows 192.168.1.x

The CF kit applies its bundled cloud-config automatically during `genesis
deploy`. The override ops file (`manifests/wayne/cf-cloud-config-net-override.yml`)
must be wired into the genesis environment YAML via an ops-file param, or
applied manually before the deploy. Confirm it overrides the upstream reserved
range:

```bash
bosh int manifests/cf/cloud-config.yml \
  -o manifests/wayne/cf-cloud-config-net-override.yml \
  | grep -A20 'subnets'
```

The output must show `10.64.64.x` ranges, not `192.168.1.x`.

---

## Key addresses

| Service | IP / URL |
|---------|----------|
| mgmt-BOSH | `https://10.64.64.10:25555` |
| env-BOSH | `https://10.64.64.12:25555` |
| HAProxy | `10.64.64.50` |
| CF API | `https://api.system.ocf.wayne.lab.fivetwenty.io` |
| CF apps domain | `*.apps.ocf.wayne.lab.fivetwenty.io` → 10.64.64.50 |

---

## Next steps

With CF running and smoke tests passing, proceed to:

1. **Cloudflare tunnel + DNS** — see `05-stratos-push.md` (W5i + W5j):
   bring up the `ocfp-wayne` cloudflared tunnel on the bastion, then cut
   over DNS records `ocf.wayne.lab.fivetwenty.io` and
   `*.apps.ocf.wayne.lab.fivetwenty.io` in the `lab.fivetwenty.io` zone to
   route public traffic through the tunnel to HAProxy at `10.64.64.50`.

2. After DNS cutover completes, the CF API is reachable from the internet
   at `https://api.system.ocf.wayne.lab.fivetwenty.io` and Stratos can be pushed
   to the `system/stratos` space.
