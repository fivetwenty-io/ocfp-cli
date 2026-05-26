# Wayne env-BOSH and CF Deploy

Deploys the Wayne env-BOSH director (10.64.64.12) via mgmt-BOSH, then deploys
Cloud Foundry on that env-BOSH director. Both deploys are hard user gates.

All commands run **on the bastion** (`ubuntu@10.64.64.3`, reachable from the
operator via its tailscale address). genesis lives at `~/.genesis/genesis`; the
deployment repos are `~/ocfp/deployments/{bosh,cf}`; the inception vault is
local at `http://127.0.0.1:8234` (safe target `ocfp-lab-wayne-inception`). This
is the SDN-gateway model: the bloc gateway and DNS are the SDN host service at
`10.64.64.1` (the PVE host firewall is opened for VM→`.1:{8006,53}`), and the
bastion is an ephemeral jumpbox, never the gateway or DNS.

> **Guard every genesis run with `ulimit -v 6291456`.** The dev kits can recurse
> and OOM the bastion; the virtual-memory cap turns a runaway into a clean fail.

---

## Prerequisites

Complete all of these before running any step below:

- [Wayne Bastion Bringup](02-bastion-bringup.md) — bastion at 10.64.64.3 running and tooled
- [mgmt-BOSH Deploy](03-mgmt-bosh-deploy.md) — mgmt-BOSH at 10.64.64.10 healthy
- Inception vault unlocked (`safe -T ocfp-lab-wayne-inception status` → unsealed)
- The dev kits staged on the bastion (see Step 6 and Step 1 of Part 2)

Verify mgmt-BOSH is healthy from the bastion:

```bash
cd ~/ocfp/deployments/bosh
ulimit -v 6291456
~/.genesis/genesis ocfp-lab-wayne-mgmt bosh --self env
```

Expected: director line with `Name: ocfp-lab-wayne-mgmt`, URL
`https://10.64.64.10:25555`.

### Key addresses

| Service | IP / URL |
|---------|----------|
| Bastion (SDN / operator) | `10.64.64.3` / tailscale FQDN |
| SDN gateway + DNS | `10.64.64.1` |
| mgmt-BOSH | `https://10.64.64.10:25555` |
| env-BOSH (ocf director) | `https://10.64.64.12:25555` |
| HAProxy (CF ingress, HTTP) | `10.64.64.20:80` |
| gorouter (CF, HTTPS) | `10.64.64.39:443` |
| CF API | `https://api.system.ocf.wayne.lab.fivetwenty.io` |
| CF apps domain | `*.apps.ocf.wayne.lab.fivetwenty.io` |

---

## Part 1: env-BOSH Deploy

> **HARD GATE — W5f.** Do not proceed past this section until mgmt-BOSH is
> confirmed healthy and you are ready to commit 30-50 minutes to the deploy.

The env-BOSH director is a `bosh` deployment (kit `dev`, iaas `pve`) defined at
`bosh/ocfp-lab-wayne-ocf.yml`. genesis sends it to mgmt-BOSH via the
`bosh_env: ocfp-lab-wayne-mgmt@...` cross-deploy ref; this is a standard
`bosh deploy`, not a create-env.

### Step 1 — Change to the bosh deployments directory

```bash
cd ~/ocfp/deployments/bosh
ulimit -v 6291456
```

All genesis commands in Part 1 run from this directory.

### Step 2 — Vault secrets prep

```bash
~/.genesis/genesis ocfp-lab-wayne-ocf add-secrets
```

The env carries `genesis.entomb: false` (the lab keeps generated secrets as
vault refs rather than entombing them into the ocf director Credhub — entombing
aborts on the empty CPI password, since the PVE CPI authenticates with an API
token, not a password).

### Step 3 — Manifest dry-run

```bash
~/.genesis/genesis ocfp-lab-wayne-ocf manifest > /tmp/ocf-manifest.yml
```

Key assertions:

| Field | Required value | Reason |
|-------|---------------|--------|
| director `static_ip` | `10.64.64.12` | `.10` is mgmt-BOSH — collision |
| availability zone | `ocfp-lab-wayne-ocf-z` | kit `-z1` default is overridden to `-z` |
| subnet `dns` | `10.64.64.1` | per-subnet dns must be set on ocfp-0/1/2 |

If the director IP shows `.10`, the ocf reserved-ips were not made distinct from
mgmt — see Troubleshooting.

### Step 4 — Deploy env-BOSH

```bash
~/.genesis/genesis ocfp-lab-wayne-ocf deploy -y
```

Typical duration: **15–30 minutes**. genesis sends the deployment to mgmt-BOSH;
BOSH creates the env-BOSH VM via the PVE CPI and uploads a stemcell.

### Step 5 — Verify env-BOSH and upload the bosh-dns runtime config

```bash
~/.genesis/genesis ocfp-lab-wayne-ocf bosh --self env
```

Expected: `Name: ocfp-lab-wayne-ocf`, `URL: https://10.64.64.12:25555`.

The ocf director needs the bosh-dns runtime config before any CF deploy (CF's
bosh-dns jobs require it; without it CF aborts with "No matching runtime
configurations defined"):

```bash
~/.genesis/genesis ocfp-lab-wayne-ocf do runtime-config dns -y
~/.genesis/genesis ocfp-lab-wayne-ocf bosh --self configs   # confirm a runtime config named ...bosh.dns
```

---

## Part 2: CF Deploy

> **HARD GATE — W5g.** Do not proceed past this section until env-BOSH is
> confirmed healthy (Step 5 passed) and you are ready to commit 1-2 hours to
> the CF deploy.

The CF deployment is `cf/ocfp-lab-wayne-ocf.yml` (genesis env
`ocfp-lab-wayne-ocf`, deployment type `cf`). The BOSH deployment name on the ocf
director is `ocfp-lab-wayne-ocf-cf`.

### Step 6 — Stage the dev CF kit with PVE patches

The released CF kit hard-wires external RDS/S3/AWS load balancers and aborts on
`iaas: pve`. The lab uses a **dev CF kit** carrying PVE patches (internal
postgres DB, internal WebDAV blobstore, no-op LB vm_extensions). Link it as the
`dev` kit:

```bash
ls -d ~/kits/cf-genesis-kit          # the patched dev kit
ls -l ~/ocfp/deployments/cf/dev      # genesis dev-kit symlink -> ~/kits/cf-genesis-kit
```

The dev kit's PVE patches (all documented for upstreaming via kit-dev-expert):

| Patch | What it does |
|-------|--------------|
| `hooks/blueprint` pve case | maps pve to the internal WebDAV blobstore (no external object store) |
| `hooks/blueprint` external skip | skips external-db/external-db-prep/external-blobstore for pve → internal postgres `database` IG |
| `ocfp/pve/{ocf,azs,blobstore}.yml` | PVE overlay set; `azs.yml` re-AZs every IG (incl. database, singleton-blobstore, haproxy) to the single `-z` AZ |
| `ocfp/trusted-certs.yml` | cflinuxfs3-rootfs-setup stanza removed (lab is cflinuxfs4-only; the fs3 job orphaned with no release) |

Confirm the cf deployment's `.genesis/config` targets the inception vault (not a
stale `:8201` target):

```bash
grep -A3 secrets_provider ~/ocfp/deployments/cf/.genesis/config   # url http://127.0.0.1:8234
```

### Step 7 — Author and upload the workload cloud config

The CLI does not yet generate a PVE workload cloud config for the ocf director
(the AWS path hand-maintains the equivalent). Author it once at
`bosh/configs/cloud/ocfp-lab-wayne-ocf.yml` and upload it to the ocf director as
a **named** cloud config `ocfp-lab-wayne-ocf-cf`:

```bash
cd ~/ocfp/deployments/bosh
ulimit -v 6291456
yes | ~/.genesis/genesis ocfp-lab-wayne-ocf bosh --self \
  update-config --type cloud --name ocfp-lab-wayne-ocf-cf \
  configs/cloud/ocfp-lab-wayne-ocf.yml
```

The workload config defines the `ocfp-lab-wayne-ocf-cf` network (lvnet001,
10.64.64.0/18, gw/dns `.1`, static `.20-.30`, dynamic `.31-.50`), the `*-dev`
vm_types and disk_types, and **no-op vm_extensions** for the five cf-deployment
extensions the manifest references (`cf-system-apps-lb`, `cf-ssh-lb`,
`cf-tcp-lb`, `cf-router-network-properties`, `cf-tcp-router-network-properties`,
`diego-ssh-proxy-network-properties`, `50GB_ephemeral_disk`,
`100GB_ephemeral_disk`). It intentionally omits `azs`/`compilation` — the
director's own cloud config supplies those, and BOSH merges named configs.

> **Compilation VM sizing.** Heavy CF compiles (cloud_controller_ng, capi,
> mariadb_connector_c) OOM the kit's default 4 GB compilation VM, which shows up
> as `Timed out sending 'get_task' ... after 45 seconds` mid-compile. Bump
> `vm-compilation` in the ocf director cloud config to ≥8 GB / 4 cpu before
> deploying CF (the host has ample capacity):
>
> ```bash
> ~/.genesis/genesis ocfp-lab-wayne-ocf bosh --self config \
>   --type cloud --name ocfp-lab-wayne-ocf.bosh.director --json \
>   | python3 -c 'import sys,json;print(json.load(sys.stdin)["Tables"][0]["Rows"][0]["content"])' \
>   > /tmp/director-cc.yml
> # edit vm-compilation cloud_properties: cpu 4, ram 8192, disk 65536
> yes | ~/.genesis/genesis ocfp-lab-wayne-ocf bosh --self \
>   update-config --type cloud --name ocfp-lab-wayne-ocf.bosh.director /tmp/director-cc.yml
> ```

### Step 8 — CF secrets prep

```bash
cd ~/ocfp/deployments/cf
ulimit -v 6291456
~/.genesis/genesis ocfp-lab-wayne-ocf add-secrets
```

The env seeds two trusted-CA paths (`certs/org:ca`, `certs/dbs:ca`) with a
self-signed CA and overrides the Terraform-sourced FQDN refs
(`meta.ocfp.cf.fqdns.*`) with literals, since the lab has no Terraform vault
tree. The env carries `genesis.entomb: false` for the same reason as env-BOSH.

### Step 9 — Manifest dry-run

```bash
~/.genesis/genesis ocfp-lab-wayne-ocf manifest > /tmp/cf-manifest.yml
grep -m1 'system_domain' /tmp/cf-manifest.yml          # system.ocf.wayne.lab.fivetwenty.io
```

| Field | Required value |
|-------|---------------|
| `system_domain` | `system.ocf.wayne.lab.fivetwenty.io` |
| cf networks | `ocfp-lab-wayne-ocf-cf` (NOT `ocfp-lab-wayne-ocf-ocf`) |
| every IG `azs` | `[ocfp-lab-wayne-ocf-z]` |

The cf network params are pinned to `ocfp-lab-wayne-ocf-cf` because the env name
already ends in `-ocf`; the kit's `(( concat genesis.env "-ocf" ))` would
otherwise double it.

### Step 10 — Deploy CF

```bash
cd ~/ocfp/deployments/cf
nohup bash -c 'ulimit -v 6291456; ~/.genesis/genesis ocfp-lab-wayne-ocf deploy -y \
  > /tmp/cf-deploy.log 2>&1; echo CF_DEPLOY_EXIT=$? >> /tmp/cf-deploy.log' >/dev/null 2>&1 &
```

The largest deploy in the Wayne sequence:

- **Duration:** 1–2 hours (154 packages compile on first run, then ~16 VMs)
- 16 instance groups: api, cc-worker, credhub, database (internal postgres),
  diego-api, diego-cell, doppler, haproxy, log-api, log-cache, nats, router,
  scheduler, singleton-blobstore (internal WebDAV), tcp-router, uaa
- Monitor: `tail -f /tmp/cf-deploy.log`; `grep CF_DEPLOY_EXIT /tmp/cf-deploy.log`

> **Expected non-fatal exit 1.** After the BOSH deploy SUCCEEDS (all tasks
> "done"), genesis exits 1 on the post-deploy step with `Failed to set
> deployment audit data in exodus: 500 ... JSON string value exceeds allowed
> length`. Genesis 3.2 stores a ~1.5 MB base64 manifest artifact in exodus,
> which the dev/strongbox vault rejects for size. The **KV exodus metadata
> (admin creds, domains) still writes**, so CF is fully deployed and
> discoverable — only the manifest archival fails. Treat a successful bosh
> deploy with only this audit-write error as success.

### Step 11 — Verify CF

```bash
cd ~/ocfp/deployments/bosh
~/.genesis/genesis ocfp-lab-wayne-ocf bosh --self -d ocfp-lab-wayne-ocf-cf instances --ps
```

Expected: 16 instances, every process `running`, none `failing`.

Validate the CF API directly (there is no wildcard DNS yet — see Next steps — so
target the gorouter by adding a hosts entry, and note HAProxy currently fronts
only HTTP `:80`, so use the gorouter's HTTPS `:443`):

```bash
echo "10.64.64.39 api.system.ocf.wayne.lab.fivetwenty.io login.system.ocf.wayne.lab.fivetwenty.io uaa.system.ocf.wayne.lab.fivetwenty.io" \
  | sudo tee -a /etc/hosts
PW=$(safe get secret/exodus/ocfp-lab-wayne-ocf/cf:admin_password)
cf api https://api.system.ocf.wayne.lab.fivetwenty.io --skip-ssl-validation
cf auth admin "$PW"
cf orgs
```

Full end-to-end (exercises CC + internal blobstore + diego staging + cell +
routing):

```bash
cf create-org lab && cf create-space test -o lab && cf target -o lab -s test
mkdir -p /tmp/app && echo hello > /tmp/app/index.html && echo '---' > /tmp/app/Staticfile
( cd /tmp/app && cf push lab-smoke -b staticfile_buildpack -m 64M --no-route )
cf map-route lab-smoke apps.ocf.wayne.lab.fivetwenty.io --hostname lab-smoke
curl -s -H 'Host: lab-smoke.apps.ocf.wayne.lab.fivetwenty.io' http://10.64.64.39/   # -> hello
cf delete lab-smoke -r -f
```

> The bosh `smoke-tests` errand will FAIL its SynchronizedBeforeSuite with a 60s
> API-target timeout until wildcard DNS exists — the errand VM cannot resolve
> `*.system`/`*.apps`. This is a DNS gap, not a CF defect; prove CF with the
> direct-target steps above.

---

## Troubleshooting

### env-BOSH: PVE CPI 401

The CPI authenticates with an API token. Confirm the token at
`secret/config/ocfp-lab-wayne/ocf/cpi/pve:api_token` is the `ocfp-cpi@pve!wayne`
token (host `pve.sm-0.lab.fivetwenty.io`, port 8006), not a stale token.

### env-BOSH: director IP collides with mgmt (.10)

ocf's reserved-ips must be distinct from mgmt's `.10`. Set the ocf bosh/director
IP to `.12` on all three subnets:

```bash
for s in ocfp-0 ocfp-1 ocfp-2; do
  safe set secret/config/ocfp-lab-wayne/ocf/net/subnets/$s/reserved-ips \
    bosh_ip=10.64.64.12 director_ip=10.64.64.12 ip=10.64.64.12
done
```

### CF: cloud-config check fails on a vm_extension/vm_type

The manifest references a vm_extension or vm_type the workload cloud config
omits. Add it (no-op for LB/network-properties/ephemeral-disk extensions) to
`bosh/configs/cloud/ocfp-lab-wayne-ocf.yml` and re-run the update-config from
Step 7, then redeploy. Compiled packages persist (`reuse_compilation_vms`).

### CF: get_task timeout during compilation

A compilation VM's agent went unreachable mid-compile — almost always OOM on an
undersized compilation VM. Bump `vm-compilation` to ≥8 GB (Step 7 sizing note)
and redeploy.

### CF: manifest gen fails on missing external-db / s3 / fqdn secrets

The dev kit's PVE patches (Step 6) skip external-db/external-blobstore and the
env overrides the Terraform FQDN refs. If these errors appear, the dev kit is
not linked (`cf/dev` symlink missing) or its blueprint patch is absent.

### CF: "Instance group X must specify availability zone ..."

An IG kept its cf-deployment base AZ (`z1`/`z2`). The dev kit's
`ocfp/pve/azs.yml` must re-AZ that IG to `(( grab meta.ocfp.azs ))` — confirm
database, singleton-blobstore, and haproxy are covered.

---

## Next steps

With CF running and the direct-target validation passing, proceed to:

1. **Cloudflare tunnel + wildcard DNS** — see `05-stratos-push.md`: bring up the
   `ocfp-wayne` cloudflared tunnel on the bastion and cut over
   `*.apps.ocf.wayne.lab.fivetwenty.io` and the `system.ocf...` records to route
   public traffic to CF. Until then the bosh `smoke-tests` errand cannot pass.

2. **HAProxy TLS (:443)** — the `self-signed` feature generates `haproxy_ssl` but
   HAProxy currently binds only `:80`; wire the PEM into the HAProxy HTTPS
   frontend (or document that the gorouter serves `:443` directly on this lab).

3. After DNS cutover, push Stratos to the `system/stratos` space.
