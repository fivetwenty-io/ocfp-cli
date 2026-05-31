# Wayne Bastion Bringup

Brings up VMID 101 (`ocfp-lab-wayne-bastion`) on sm-0 and provisions it with
all operator tooling. Bastion IP: `10.64.64.3/18` on `lvnet001`.

The bastion is provisioned via the OCFP CLI directly — not via a genesis kit.

---

## 1. Prerequisites

### 1.1 CPI account

PVE CPI service account and token must exist before running this runbook.
Complete [CPI Account Setup](01-cpi-account-setup.md) first.

### 1.2 Operator shell environment

Export these variables in your shell before any step in this runbook.
Values come from the PVE token created in the CPI account setup.

```bash
export PVE_HOST=sm-0.lab.fivetwenty.io   # tailscale FQDN for operator context
export PVE_NODE=pve                        # PVE node name (verify: pvesh get /nodes)
export PVE_TOKEN_ID=ocfp-cpi@pve\!wayne   # token ID including user + token name
export PVE_TOKEN_SECRET=<secret>           # token secret captured during creation

export PVE_BRIDGE=lvnet001                 # SDN bridge for bastion NIC
export PVE_STORAGE_POOL=data               # lvmthin pool for VM root disks
export PVE_ISO_STORAGE=local               # storage holding cloud-init ISO content
```

Verify connectivity to sm-0 over tailscale before continuing:

```bash
ssh root@sm-0 hostname
```

### 1.3 Tailscale on operator Mac

Your Mac must be enrolled in the tailnet so that `sm-0.lab.fivetwenty.io`
resolves to sm-0's tailscale IP. Verify with:

```bash
ping -c 1 sm-0.lab.fivetwenty.io
```

### 1.4 PVE CPI dev tarball

Confirm the dev tarball is present locally:

```bash
ls ~/w/proxmox/bosh-pve-cpi-release/bosh-pve-cpi-dev-*.tgz
```

The most recent timestamped build is used in later steps. At time of writing
that is `bosh-pve-cpi-dev-20260521165224.tgz`. Rebuild if you need a fresher
build:

```bash
cd ~/w/proxmox/bosh-pve-cpi-release
make dev-release
```

### 1.5 OCFP CLI built

```bash
cd ~/w/fivetwenty/studios/ocfp/src/clis/ocfp
go build -o ocfp .
export PATH="$PWD:$PATH"
ocfp --version
```

---

## 2. Lab-side checks (one-time, already done in W5c)

These checks confirm the lab is ready. They are read-only — no write operations.

### 2.1 Confirm lvnet001 is active

```bash
ssh root@sm-0 pvesh get /cluster/sdn/vnets/lvnet001
```

Expected: JSON object with `type: vnet`, `zone` matching the lvnet zone.
The subnet `10.64.64.0/18` must be routed on this vnet.

### 2.2 Confirm VMID 100 (drgao) is reserved

VMID 100 is the drgao template. The bastion uses VMID 101. Confirm 100 exists
so you don't accidentally clobber it:

```bash
ssh root@sm-0 qm config 100 | head -5
```

### 2.3 Confirm cloud-init template ISO is in `local` storage

```bash
ssh root@sm-0 pvesh get /nodes/pve/storage/local/content --content=iso 2>/dev/null | grep -i ubuntu
```

The Ubuntu Noble cloud image must be present. If absent, the OCFP CLI's
`ProvisionTemplate` path downloads it automatically on the next `ocfp bastion
init` run.

---

## 3. PVE-side snippet creation

PVE 9.x's storage upload API forbids `content=snippets` — the CLI does not
generate or upload cloud-init snippet files. Per-VM tailscale config is
delivered via SMBIOS fields instead (see §6.1). The cloud-init snippet must be
authored manually and copied to the PVE host before cloning the bastion VM.

### 3.1 What the snippet contains

Create the cloud-init user-data file on your Mac:

```yaml
# ocfp-lab-wayne-bastion-101-user.yml
#cloud-config
hostname: ocfp-lab-wayne-bastion
manage_etc_hosts: true
users:
  - name: ubuntu
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - <your-operator-ssh-public-key>
write_files: []
runcmd: []
```

The snippet sets hostname and injects the operator SSH key. Tailscale auth and
bloc config are delivered via SMBIOS at VM create time — not through this file.

### 3.2 Upload the snippet to sm-0

PVE snippets must live in `/var/lib/vz/snippets/` on the PVE host. Copy the
file directly over SSH:

```bash
scp ocfp-lab-wayne-bastion-101-user.yml \
    root@sm-0:/var/lib/vz/snippets/ocfp-lab-wayne-bastion-101-user.yml
```

Verify the upload:

```bash
ssh root@sm-0 ls -la /var/lib/vz/snippets/ | grep wayne-bastion
```

### 3.3 Inspect the snippet on the host

```bash
ssh root@sm-0 cat /var/lib/vz/snippets/ocfp-lab-wayne-bastion-101-user.yml
```

Confirm `hostname: ocfp-lab-wayne-bastion` is present. Tailscale config is NOT
in this file — it is injected via SMBIOS at clone time (§4.1).

---

## 4. Bastion provisioning

### 4.1 Clone and configure the VM

If the bastion VM (VMID 101) does not yet exist, clone it from the template
and attach the snippet:

```bash
# Stop the drgao template if it is running
ssh root@sm-0 qm status 9001
# If running: ssh root@sm-0 qm stop 9001

# Clone template 9001 → VMID 101
ssh root@sm-0 qm clone 9001 101 --name ocfp-lab-wayne-bastion --full --storage data

# Wire the network and cloud-init snippet
ssh root@sm-0 qm set 101 \
  --net0 virtio,bridge=lvnet001 \
  --ipconfig0 ip=10.64.64.3/18,gw=10.64.64.1 \
  --nameserver 1.1.1.1 \
  --cicustom "user=local:snippets/ocfp-lab-wayne-bastion-101-user.yml"

# Start the bastion
ssh root@sm-0 qm start 101
```

### 4.2 Provision tooling via OCFP CLI

Once the bastion VM is reachable (wait ~60–90 s for cloud-init to finish):

```bash
ocfp bastion provision --bloc ocfp-lab-wayne
```

This command:

1. SSHs to the bastion at `10.64.64.3` (or via the tailscale FQDN after §6).
2. SCPs `scripts/provision/bastion` from the OCFP CLI repo to
   `/tmp/provision-bastion.pl` on the bastion.
3. Executes `perl /tmp/provision-bastion.pl | tee ~/provision.log` with the
   PVE env vars injected as the remote environment.

The provision script installs these tools in order:

| Stage | Tool | Method |
|-------|------|--------|
| A | apt build deps (curl, git, unzip, etc.) | `apt-get install` |
| B | yq v4 | GitHub release binary |
| C | safe | GitHub release binary |
| D | vault | HashiCorp zip release |
| E | bosh v7+ | GitHub release binary |
| F | cf v8+ | GitHub release tarball |
| G | cloudflared | GitHub release binary |
| H | AWS CLI v2 | official `awscli.amazonaws.com` zip installer |
| I | genesis v3.2.x-dev | git clone + `~/.local/bin` symlink |
| J | summary | prints version table |

The script is idempotent. Re-running it skips any stage whose binary is already
on `PATH`.

Provision typically takes 5–10 minutes on first run. Tail the log on the
bastion in a second terminal if you want progress:

```bash
ssh ubuntu@10.64.64.3 tail -f ~/provision.log
```

### 4.3 Initialize the bastion (`ocfp bastion init`)

`provision` only installs tools. `init` is the second half of the flow — it
builds the canonical operator filesystem layout and wires configuration, exactly
as it does for AWS and StackIt bastions. Skipping it leaves the bastion without
`~/ocfp`, `~/ops`, `~/deployments`, or `~/bin`, and without the vault/genesis
wiring the deploy steps assume.

#### Prerequisite: `bastion_ip`

`init` runs from your operator machine and SSHs to the bastion. From the Mac the
bastion is reachable only over tailscale (not the SDN IP `10.64.64.3`), so the
bloc must declare how to reach it. Set `bastion_ip` under the `ocfp-lab-wayne`
bloc in `~/.ocfp/config.pve.yml` to the bastion's tailscale FQDN (or IP):

```yaml
blocs:
  ocfp-lab-wayne:
    <<: *pve_common
    bastion_ip: ocfp-wayne-bastion.<tailnet>.ts.net   # or the 100.x tailscale IP
    # ...
```

Without it `init` fails fast with a message naming this remedy. Alternatively
export `PVE_BASTION_IP` for a one-off run.

#### Preview, then run

Dry-run first to see the planned phases without touching the bastion:

```bash
ocfp bastion init --bloc ocfp-lab-wayne --dry-run
ocfp bastion init --bloc ocfp-lab-wayne
```

`init` is idempotent: tool phases skip any binary already installed by
`provision`, so it is safe to re-run. It creates:

- The `~/ocfp` tree (`cli`, `deployments`, `releases`, `artifacts`, `kits`),
  plus `~/bin`, `~/.ocfp/logs`, and `~/.genesis`.
- The `~/ops → ~/ocfp` and `~/deployments → ~/ocfp/deployments` symlinks, and
  the `~/bin/ocfp` binary symlink.
- OCFP config files and the genesis logging config.
- Vault inception (skips if already initialized), then vault populate.
- `ocfp configure` — clones the deployment repos into `~/ocfp/deployments`.
- Genesis secrets providers pointed at the inception vault.
- The `~/.ocfp/provisioned` marker (written only on full success).

#### Verify

```bash
ssh ubuntu@10.64.64.3 'ls -ld ~/ocfp ~/ops ~/deployments ~/bin && test -f ~/.ocfp/provisioned && echo provisioned'
```

Expect the four paths to exist (`~/ops` a symlink to `~/ocfp`) and
`provisioned` printed. This matches the layout on an AWS/StackIt bastion.

### 4.4 Copy CPI dev tarball to bastion

After provisioning completes, copy the PVE CPI dev tarball to the bastion so
the mgmt-BOSH `create-env` step can reference it:

```bash
TARBALL=$(ls -t ~/w/proxmox/bosh-pve-cpi-release/bosh-pve-cpi-dev-*.tgz | head -1)

scp "$TARBALL" ubuntu@10.64.64.3:/var/vcap/store/bosh-pve-cpi/bosh-pve-cpi-dev.tgz
```

Then extract on the bastion:

```bash
ssh ubuntu@10.64.64.3 "
  sudo mkdir -p /var/vcap/store/bosh-pve-cpi/release
  sudo tar xzf /var/vcap/store/bosh-pve-cpi/bosh-pve-cpi-dev.tgz \
      -C /var/vcap/store/bosh-pve-cpi/release/
"
```

This satisfies the ops-file path assumption in `manifests/bosh/cpi.yml` (the
path `bosh-pve-cpi/release/` must exist before `genesis deploy
ocfp-lab-wayne-mgmt`).

---

## 5. SSH verification

### 5.1 Connect via OCFP CLI

```bash
ocfp bastion ssh --bloc ocfp-lab-wayne
```

Or connect directly:

```bash
ssh ubuntu@10.64.64.3
```

### 5.2 Verify all CLI tools are present

```bash
which genesis safe vault bosh cf cloudflared jq yq
```

All eight paths must be printed. No "command not found" lines.

### 5.3 Verify genesis version

```bash
genesis --version
```

Expected output includes `v3.2.x-dev`. If the version string shows a tagged
release instead of the dev branch, the provision script may have fetched the
wrong branch. Check:

```bash
git -C ~/genesis branch --show-current
```

Expected: `v3.2.x-dev`

---

## 6. Post-bringup state

### 6.1 Tailscale

Tailscale is configured during cloud-init firstboot via `ocfp-firstboot.service`
(seeded into the bastion template at build time — see commit `2d66dd0`). The
service reads three SMBIOS fields set by the CPI at clone time:

| dmidecode flag | SMBIOS field | Content |
|---|---|---|
| `-s system-family` | `Family` | `ocfp-bastion` (role marker) |
| `-s system-serial-number` | `Serial` | base64-encoded tailscale auth key |
| `-s system-sku-number` | `SKU` | JSON config blob (hostname, tags, routes) |

The firstboot script no-ops unless `dmidecode -s system-family` equals
`ocfp-bastion`. It then decodes `Serial` to get the auth key and calls
`tailscale up --authkey=<key>` with options from the `SKU` JSON blob.

Verify tailscale is active on the bastion:

```bash
ssh ubuntu@10.64.64.3 tailscale status
```

Expected: the bastion appears as an online node. Its tailscale FQDN is:

```
wayne-bastion.<tailnet>.ts.net
```

After tailscale is up you can also SSH via the tailscale FQDN instead of the
private IP:

```bash
ssh ubuntu@wayne-bastion.<tailnet>.ts.net
```

### 6.2 Vault

A singleton Vault instance runs on the bastion (per the blobstore decision
for this lab). Vault is installed by the provision script but must be
initialized and unsealed before use. That step is part of the mgmt-BOSH deploy
wave, not this runbook.

Confirm vault binary is present:

```bash
ssh ubuntu@10.64.64.3 vault version
```

---

## 7. Troubleshooting

### 7.1 Firstboot service failures

If the bastion comes up but tailscale is not registered, inspect the firstboot
service via the PVE console (does not require SSH):

```bash
# From sm-0
qm terminal 101
```

Once at the serial console:

```bash
journalctl -u ocfp-firstboot.service --no-pager
```

Common causes:

- SMBIOS `Family` field not set to `ocfp-bastion` — the VM was cloned without
  the SMBIOS config block. Set it via:
  ```bash
  ssh root@sm-0 qm set 101 \
    --smbios1 "base64=1,family=$(echo -n 'ocfp-bastion' | base64),serial=<base64-authkey>,sku=<base64-sku-json>"
  ssh root@sm-0 qm reboot 101
  ```
  Verify with: `ssh root@sm-0 qm config 101 | grep smbios`
- Tailscale auth key expired — generate a new key in the tailscale admin console,
  base64-encode it, and update the `serial=` field in the `smbios1` config, then
  reboot the VM.

### 7.2 Missing PVE env vars

If `ocfp bastion provision` reports missing env vars:

```bash
env | grep PVE_
```

All five vars (`PVE_HOST`, `PVE_NODE`, `PVE_TOKEN_ID`, `PVE_TOKEN_SECRET`,
`PVE_BRIDGE`) must be set. Re-export from §1.2 and re-run.

### 7.3 Provision script stage failure

The script exits with `STAGE_FAILED:<stage>` on any non-zero return. The log
on the bastion names the failing stage:

```bash
ssh ubuntu@10.64.64.3 grep STAGE_FAILED ~/provision.log
```

The script is idempotent — fix the root cause (network, missing apt mirror,
GitHub API rate limit) and re-run:

```bash
ocfp bastion provision --bloc ocfp-lab-wayne
```

### 7.4 VM unreachable after start

If `10.64.64.3` does not respond after ~120 seconds:

1. Check cloud-init applied the static IP via the PVE console:
   `qm terminal 101` → `ip a`
2. Verify the snippet is attached: `ssh root@sm-0 qm config 101 | grep cicustom`
3. If `cicustom` is missing, re-run `qm set 101 --cicustom "..."` from §4.1
   and then `qm reboot 101`.

### 7.5 Rollback

To destroy and restart from scratch:

```bash
ssh root@sm-0 qm stop 101
ssh root@sm-0 qm destroy 101 --purge
```

Then restart from §4.1. The snippet file on disk does not need to be removed
unless you want to regenerate it.

---

## 8. Summary of key values

| Item | Value |
|------|-------|
| Bastion VMID | 101 |
| Bastion IP | 10.64.64.3/18 |
| Gateway | 10.64.64.1 |
| Bridge | lvnet001 |
| Snippet | `ocfp-lab-wayne-bastion-101-user.yml` |
| Tailscale FQDN | `wayne-bastion.<tailnet>.ts.net` |
| Provision log | `~/provision.log` on bastion |
| CPI tarball path (bastion) | `/var/vcap/store/bosh-pve-cpi/bosh-pve-cpi-dev.tgz` |
| CPI release extract path | `/var/vcap/store/bosh-pve-cpi/release/` |

---

## 9. Next steps

With the bastion running and tooled, proceed to the mgmt-BOSH deploy (W5e):

```bash
cd ~/w/fivetwenty/studios/ocfp/src/deployments/fivetwenty-ocfp/bosh
genesis deploy ocfp-lab-wayne-mgmt
```

This is a hard user gate. Confirm the bastion is healthy before proceeding.
