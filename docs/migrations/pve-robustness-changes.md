# PVE Robustness Changes

This document covers breaking and behavior changes introduced by the PVE
robustness update. Read it before merging or deploying a build that includes
these changes to an existing PVE bloc.

## Breaking and behavior changes

### Tailscale provisioning is now opt-in (BREAKING)

**Who is affected:** any PVE bastion bloc that previously received Tailscale
provisioning without an explicit `tailscale.enabled` key in its bloc config.

**What changed:** `bastionTailscaleSpec` returns `nil` (no Tailscale provisioning)
when `tailscale.enabled` is absent or `false`. Prior builds treated any config
with an `auth_key` as implicitly enabled.

**Action required:** add `tailscale.enabled: true` to every bloc that should
continue receiving Tailscale provisioning.

```yaml
blocs:
  wayne-lab:
    provider: pve
    # ...
    tailscale:
      enabled: true          # required — provisioning is disabled without this
      auth_key: "tskey-auth-..."
```

Without `enabled: true`, the BOSH bastion VM is created and started normally but
`tailscale up` is never called and the node does not join the tailnet.

---

### VMID range default changed from 200 to 100 (SOFT-BREAK)

**Who is affected:** operators whose existing PVE VMIDs fall in the range
100–199 and who relied on the old default `vmid_range_start: 200` to keep BOSH
away from those IDs.

**What changed:** the default `vmid_range_start` is now `100` (aligning with
the upstream lab manifest `manifests/bosh/vars.yml`). The prior hardcoded value
was `200`, which contradicted the lab reference.

**Action required:** if you have guest VMs with VMIDs 100–199 and need to keep
BOSH from claiming IDs in that range, set `vmid_range_start` explicitly in your
bloc config:

```yaml
blocs:
  my-pve-bloc:
    provider: pve
    vmid_range_start: 200   # preserve old behavior
    vmid_range_end:   5999  # optional; default is 5999
```

Operators with no VMs in the 100–199 range are not affected.

---

### PVE auth validated at config load — both-empty is now a hard error (SOFT-BREAK)

**Who is affected:** any operator or CI pipeline that constructs a PVE bloc
config with neither API-token credentials (`auth_token` + `token_secret`) nor
username/password credentials (`username` + `password`).

**What changed:** `Config.Validate()` now calls `validatePVEAuth()` for every
PVE bloc. If both auth modes are absent, it returns `ErrPVEAuthRequired` and
the process exits before making any API calls. If both modes are present
simultaneously, a warning is printed to stderr and the API token takes precedence
(no error).

**Action required:** ensure every PVE bloc config supplies exactly one auth mode.

```yaml
# API token (preferred)
auth_token:   "root@pam!ocfp-token"
token_secret: "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"

# OR username/password (fallback)
username: "root@pam"
password: "hunter2"
```

Configs with a valid auth mode continue to work with no changes.

---

### BOSH Director default sizing bumped to 8 vCPU / 16 GiB RAM / 128 GiB disk (INFO)

**Who is affected:** new director deployments bootstrapped with the `bosh` flavor
preset after this change. Existing directors and VMs are unaffected — BOSH does
not resize running VMs unless you re-deploy.

**What changed:** the `bosh` flavor preset constants in `internal/cpi/pve/compute.go`
were updated to match the upstream lab reference sizes:

| Field | Old value | New value |
|-------|-----------|-----------|
| vCPU | 4 | 8 |
| RAM | 8 GiB | 16 GiB |
| Disk | ~100 MiB | 128 GiB |

The old disk constant was `100` in MiB (roughly 100 MiB), which was almost
certainly a unit error — a director needs at minimum 50 GiB. The new value,
`131072` MiB, equals 128 GiB. The unit is MiB throughout BOSH `cloud_properties`.

No action is required for existing directors.

---

### Five new framework packages — runtime wiring pending

The following packages were added and are fully tested, but are not yet called
by any production code path. Their lab learnings do not affect runtime behavior
until the wiring step is completed in a follow-up.

| Package | Purpose | Intended call site |
|---------|---------|------------------|
| `internal/pve/opsfiles` | Embeds and writes PVE-specific ops files (nats/hm/os-conf/pve-guest-agent) | `ocfp bootstrap` or `init bastion` staging step |
| `internal/pve/probes` | Pre-deploy UAA Flyway DB check and TCP dial probe | `commands/deploy.go` or `bastion/modes.go` pre-deploy hook |
| `internal/pve/netvalidate` | CIDR coherence validator between network and cloud-config | `commands/bootstrap.go` at config-load time |
| `internal/pve/stemcell` | Idempotent stemcell upload — skips if already present | `vault/pve_provider.go` or bootstrap flow before `bosh deploy` |
| `commands.WithResurrectionGate` | Disables resurrection before deploy, re-enables on exit | `bastion/modes.go` director bootstrap; future `ocfp deploy` command |

These packages are correct and unit-tested. Operators will not see changed
behavior from these learnings until the wiring work is merged.

---

## See also

- [PVE provider configuration](../pve.md) — bloc config reference and auth setup
- [PVE robustness architecture](../architecture/pve-robustness.md) — package layout and probe flow
- [PVE recovery commands](../commands/pve-commands.md) — `ocfp pve unstick` and the integration harness
