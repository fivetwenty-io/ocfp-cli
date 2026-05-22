# PVE CPI Account Setup — Wayne Bloc

One-time operator procedure to create the `ocfp-cpi@pve` service account on sm-0. Run this before any bastion bringup. All subsequent OCFP tooling reads the resulting token from vault.

## Why this step can't be automated

The Proxmox VE user and role database (`pveum`) lives on the hypervisor host itself, behind a root login. The OCFP CPI account does not exist yet — there is nothing for an API token to authenticate against before it is created. This is a deliberate security boundary: only a human with root SSH access to sm-0 can bootstrap the initial credential. After this step, the CPI token handles all future automation.

## Prerequisites

- SSH access as `root` to `sm-0` (via Tailscale: `ssh root@sm-0.lab.fivetwenty.io`)
- `pveum` CLI available on sm-0 (standard on PVE 8+)
- SDN zone `lvnet001` already configured on sm-0 (provides the 10.64.64.0/18 network)
- `safe` CLI installed on your operator Mac and pointed at the lab vault
- `pvesh` CLI available on sm-0 (standard on PVE 8+)
- `curl` available on your operator Mac (for remote verification)

## Step 1 — Open an SSH session to sm-0

```bash
ssh root@sm-0.lab.fivetwenty.io
```

All `pveum` commands in Steps 2–5 run in this root session.

## Step 2 — Create the PVE realm user

```bash
pveum user add ocfp-cpi@pve \
  --comment "OCFP CPI service account — wayne bloc"
```

The `@pve` realm uses PVE's built-in authentication. No LDAP or PAM integration is needed.

Verify the user exists:

```bash
pveum user list | grep ocfp-cpi
```

Expected output contains a line with `ocfp-cpi@pve`.

## Step 3 — Create the OCFPCpi role with required privileges

The CPI needs permissions to create, clone, configure, start, stop, and destroy VMs; allocate and attach persistent disks; and use the SDN network.

```bash
pveum role add OCFPCpi --privs \
"Datastore.AllocateSpace,\
Datastore.Audit,\
Pool.Allocate,\
SDN.Use,\
Sys.Audit,\
Sys.Console,\
Sys.Modify,\
VM.Allocate,\
VM.Audit,\
VM.Clone,\
VM.Config.CDROM,\
VM.Config.Cloudinit,\
VM.Config.CPU,\
VM.Config.Disk,\
VM.Config.HWType,\
VM.Config.Memory,\
VM.Config.Network,\
VM.Config.Options,\
VM.Migrate,\
VM.Monitor,\
VM.PowerMgmt"
```

Verify the role was created:

```bash
pveum role list | grep OCFPCpi
```

## Step 4 — Grant the role on relevant paths

Grant `OCFPCpi` at root (`/`) so the account reaches all pools, storages, and VMs. Then add explicit grants on the two persistent-disk storage pools to ensure disk allocation works regardless of pool inheritance settings.

```bash
# Root path — covers VMs, SDN, pools
pveum acl modify / \
  --users ocfp-cpi@pve \
  --roles OCFPCpi

# Explicit grants on named storage pools
pveum acl modify /storage/data \
  --users ocfp-cpi@pve \
  --roles OCFPCpi

pveum acl modify /storage/zfs-1 \
  --users ocfp-cpi@pve \
  --roles OCFPCpi

pveum acl modify /storage/local \
  --users ocfp-cpi@pve \
  --roles OCFPCpi
```

Verify the ACL entries:

```bash
pveum acl list | grep ocfp-cpi
```

Expected: entries for `/`, `/storage/data`, `/storage/zfs-1`, and `/storage/local`.

## Step 5 — Create the API token

The token name `wayne` is the bloc-scoped suffix. The resulting token ID is `ocfp-cpi@pve!wayne`.

```bash
pveum user token add ocfp-cpi@pve wayne --privsep 0
```

The output looks like:

```
┌──────────────┬──────────────────────────────────────┐
│ key          │ value                                │
╞══════════════╪══════════════════════════════════════╡
│ full-tokenid │ ocfp-cpi@pve!wayne                   │
├──────────────┼──────────────────────────────────────┤
│ info         │ {"privsep":"0"}                      │
├──────────────┼──────────────────────────────────────┤
│ value        │ 3f8b2a1d-5c9e-4f0a-b7d2-1e6a4c8f9b0d │
└──────────────┴──────────────────────────────────────┘
```

**The token secret (`value`) is shown only once.** Copy it now.

- Token ID: `ocfp-cpi@pve!wayne`
- Token secret: `3f8b2a1d-5c9e-4f0a-b7d2-1e6a4c8f9b0d` (example — yours will differ)

`--privsep 0` means the token inherits all user privileges. This is required so the CPI can operate without per-privilege token grants.

## Step 6 — Verify the token works on the PVE host

While still on sm-0, confirm the token authenticates and can reach the API:

```bash
curl -sk \
  -H "Authorization: PVEAPIToken=ocfp-cpi@pve!wayne=3f8b2a1d-5c9e-4f0a-b7d2-1e6a4c8f9b0d" \
  https://localhost:8006/api2/json/version \
  | python3 -m json.tool
```

Expected response (PVE version will match your install):

```json
{
  "data": {
    "release": "9",
    "repoid": "916486b4",
    "version": "9.0"
  }
}
```

Confirm the token can list VMs on the `pve` node:

```bash
curl -sk \
  -H "Authorization: PVEAPIToken=ocfp-cpi@pve!wayne=3f8b2a1d-5c9e-4f0a-b7d2-1e6a4c8f9b0d" \
  https://localhost:8006/api2/json/nodes/pve/qemu \
  | python3 -m json.tool | head -30
```

Expected: a JSON object with a `data` array containing your lab VMs. HTTP 200 and parseable JSON confirms auth is working.

You can now close the sm-0 SSH session.

## Step 7 — Record the token in vault

From your operator Mac (vault must be reachable via Tailscale):

```bash
safe set secret/ocfp/wayne/pve \
  cpi_user="ocfp-cpi@pve" \
  cpi_token_id="ocfp-cpi@pve!wayne" \
  cpi_token_secret="3f8b2a1d-5c9e-4f0a-b7d2-1e6a4c8f9b0d" \
  cpi_node="pve" \
  cpi_host="sm-0.lab.fivetwenty.io" \
  cpi_port="8006"
```

Verify the write:

```bash
safe get secret/ocfp/wayne/pve
```

Expected: all six fields printed without error.

## Step 8 — Export shell environment variables

Set these in your current shell session before running any subsequent `ocfp` or `bosh` commands. Replace the token secret with your actual value from Step 5.

```bash
export PVE_HOST="sm-0.lab.fivetwenty.io"
export PVE_NODE="pve"
export PVE_TOKEN_ID="ocfp-cpi@pve!wayne"
export PVE_TOKEN_SECRET="3f8b2a1d-5c9e-4f0a-b7d2-1e6a4c8f9b0d"
export PVE_BRIDGE="lvnet001"
export PVE_STORAGE_POOL="data"
export PVE_ISO_STORAGE="local"
```

These match the variable names exported by `ocfp bastion init` and consumed by the provision script. Add them to your `~/.zshrc` or a `~/.env.wayne` file that you source at session start to avoid re-entering them.

## Step 9 — Validate from operator Mac (remote verification)

With the env vars set and Tailscale connected, confirm the token works from your Mac:

```bash
pvesh get /version \
  --apitoken "PVEAPIToken=${PVE_TOKEN_ID}=${PVE_TOKEN_SECRET}" \
  --output-format=json
```

If `pvesh` is not installed on your Mac, use `curl`:

```bash
curl -sk \
  -H "Authorization: PVEAPIToken=${PVE_TOKEN_ID}=${PVE_TOKEN_SECRET}" \
  "https://${PVE_HOST}:8006/api2/json/version" \
  | python3 -m json.tool
```

Expected: the PVE version JSON shown in Step 6. An HTTP 200 response confirms end-to-end connectivity from your Mac through Tailscale to the PVE API.

## Rollback

If you need to remove the account:

```bash
# On sm-0 — removes user, all tokens, and all ACL entries for that user
pveum user delete ocfp-cpi@pve

# From operator Mac — remove vault entries
safe delete secret/ocfp/wayne/pve
```

## What comes next

With the CPI account in vault, proceed to [Template Prep](./02-template-prep.md) to clone the bastion template and configure the Wayne bastion VM.
