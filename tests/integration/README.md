# PVE Integration Test Harness

This package contains the live-cluster integration tests for the BOSH CPI
lifecycle. Tests in this directory use the `integration` build tag and are
never included in the standard unit-test lane (`go test ./...`).

The harness exercises the full 16-step CPI sequence — `create_stemcell`
through `delete_stemcell` — against a real Proxmox VE cluster and asserts
out-of-band PVE state via `PVEVerifier` between create and delete steps.

---

## Prerequisites

Before running any integration target, ensure:

1. A Proxmox VE cluster is reachable from the machine running the tests.
2. The `bin/cpi` binary is compiled (`make build` or `go build -o bin/cpi ./cmd/cpi`).
3. A BOSH stemcell tarball (`.tgz`) is available locally.
4. Auth credentials for the PVE API are ready (token or user/password).

---

## Quick Start

```bash
export OCFP_PVE_HOST=pve.example.com
export OCFP_PVE_NODE=pve
export OCFP_PVE_TOKEN="PVEAPIToken=root@pam!mytoken=xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
export OCFP_STEMCELL_PATH=/path/to/bosh-stemcell.tgz
export OCFP_VM_STORAGE=local-lvm
export OCFP_DISK_STORAGE=local-lvm
export OCFP_STEMCELL_STORAGE=local
export OCFP_ISO_STORAGE=local

make integration-pve
```

To run only the 16-step lifecycle sequence:

```bash
make integration-pve-lifecycle
```

To validate config loading without hitting a live cluster:

```bash
make integration-pve-dry-run
```

To invoke `go test` directly with custom flags:

```bash
go test -tags=integration -count=1 -v -timeout 45m \
    -run TestPVELifecycle_16Steps \
    ./tests/integration/...
```

---

## Environment Variables

### Required

| Variable | Description |
|---|---|
| `OCFP_PVE_HOST` | PVE API hostname, e.g. `pve.example.com`. |
| `OCFP_PVE_NODE` | PVE cluster node name, e.g. `pve`. |
| `OCFP_STEMCELL_PATH` | Absolute path to BOSH stemcell tarball (`.tgz`). |

### Authentication (one of the following)

| Variable | Description |
|---|---|
| `OCFP_PVE_TOKEN` | Full PVE API token string: `PVEAPIToken=<user>@<realm>!<id>=<uuid>`. |
| `OCFP_PVE_USER` + `OCFP_PVE_PASSWORD` | PVE username and password (used when `OCFP_PVE_TOKEN` is absent). |

### Storage Pools (required by the CPI config)

| Variable | Default | Description |
|---|---|---|
| `OCFP_VM_STORAGE` | _(empty)_ | Storage pool for VM disks. |
| `OCFP_DISK_STORAGE` | _(empty)_ | Storage pool for persistent disks. |
| `OCFP_STEMCELL_STORAGE` | _(empty)_ | Storage pool for stemcell images. |
| `OCFP_ISO_STORAGE` | _(empty)_ | Storage pool for ISO images. |

### Optional

| Variable | Default | Description |
|---|---|---|
| `OCFP_CPI_BIN` | `./bin/cpi` | Path to the compiled CPI binary. |
| `OCFP_PVE_PORT` | `8006` | PVE API port. |
| `OCFP_VMID_RANGE_START` | `900` | First VMID allocated by the lifecycle test. |
| `OCFP_NETWORK_BRIDGE` | `vmbr0` | PVE bridge for the test VM. |
| `OCFP_NETWORK_IP` | `192.168.1.250` | IP assigned to the test VM. |
| `OCFP_NETWORK_RANGE` | `192.168.1.0/24` | Network CIDR for the test VM. |
| `OCFP_NETWORK_GATEWAY` | `192.168.1.1` | Gateway for the test VM. |
| `OCFP_NETWORK_DNS` | `["8.8.8.8"]` | JSON array of DNS servers. |
| `OCFP_VM_CORES` | `1` | Core count for the test VM. |
| `OCFP_VM_MEMORY_MIB` | `1024` | Memory (MiB) for the test VM. |
| `OCFP_DISK_SIZE_MIB` | `1024` | Persistent disk size (MiB). |
| `OCFP_AGENT_ID` | `lifecycle-<pid>` | BOSH agent UUID for `create_vm`. |
| `OCFP_NETWORK_TEST_MODE` | _(unset / off)_ | Enable network step: `sdn` or `bridge`. |
| `DRY_RUN` | _(unset)_ | Set to `1` to run config-load smoke only (no live PVE calls). |

---

## Tier Reference

The harness is organized into four tiers that match the `ci/integration.yml`
config schema.

| Tier | Scope | Env gate |
|---|---|---|
| Tier 1 (lifecycle) | 16-step CPI lifecycle | `OCFP_PVE_HOST` + auth + stemcell |
| Tier 2 (bosh) | BOSH director deployment | Tier 1 + `OCFP_PVE_NODE` BOSH creds |
| Tier 3 (cf) | Cloud Foundry smoke tests | Tier 2 + CF vars |
| Tier 4 (light) | Lightweight sanity | Optional |

Each tier test skips silently when its required env vars are absent.

---

## Recovery — Stuck Resources

If a test run is interrupted (Ctrl-C, timeout, network loss), the test's
`t.Cleanup` handler may not fire, leaving orphan VMs or volumes on the
cluster.

**Option 1 — `ocfp pve unstick`**

```bash
ocfp pve unstick <vmid>
```

Locates the VM by VMID and issues `delete_vm` then `delete_disk` for any
attached volumes. Safe to run against already-deleted resources.

**Option 2 — manual cleanup via PVEVerifier predicates**

Use the following predicates to check state, then delete via the PVE web UI
or API:

- `VMExists(ctx, vmCID)` — `true` when the VMID is present on the node.
- `VolumeExists(ctx, diskCID)` — `true` when `<storage>:<volid>` exists in the storage pool.

Look for VMIDs in the range `OCFP_VMID_RANGE_START` to
`OCFP_VMID_RANGE_START + 99` (default 900–999).

---

## CI Guidance

Integration tests require a live PVE cluster and are **not** run on every
pull request. They belong in a gated CI lane:

- Gate on PVE availability via a CI environment variable (same as `OCFP_PVE_HOST`).
- Run `make integration-pve` in a dedicated job that has network access to the
  PVE management API.
- Set a per-job timeout of at least 30 minutes to accommodate the full 16-step
  lifecycle on a loaded cluster.
- Store PVE credentials as CI secrets; never commit them to the repository.
- Drain leftover resources after each run by checking the VMID range; consider
  running `ocfp pve unstick` as a post-job step.

The standard PR gate (`make test` / `go test ./...`) always excludes these
tests because they carry the `//go:build integration` tag.
