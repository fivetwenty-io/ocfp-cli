# PVE Commands

Commands specific to the Proxmox VE provider.

## `ocfp pve unstick <instance>`

Recovers a BOSH-managed VM whose agent has stopped responding over NATS. This
happens when the NATS connection wedges and the agent process is alive but silent —
`bosh vms` shows the VM, BOSH cannot communicate with it, and a re-deploy hangs
waiting for the agent to respond.

### What it does

1. Resolves the VM's CID from `bosh vms --json` output.
2. Extracts the PVE host address from the BOSH vars file (`--path=/pve_host`).
3. SSHes to the PVE host and runs `qm guest exec <vmid> -- /bin/sh -c "systemctl restart bosh-agent && sleep 3 && systemctl is-active bosh-agent"` inside the guest via the QEMU guest agent.
4. Verifies the agent process is active before returning.

The command never modifies BOSH state or touches the VM disk. It is safe to run
while the VM is deployed.

### Usage

```bash
ocfp pve unstick <instance>
```

Where `<instance>` is the BOSH instance name in `<job>/<uuid>` or `<job>/0` form,
as shown by `bosh vms`.

### Required environment

| Variable | Description |
|----------|-------------|
| `BOSH_ENVIRONMENT` | BOSH director alias or URL |
| `BOSH_DEPLOYMENT` | BOSH deployment name |
| `BOSH_VARS_STORE` | Path to the vars file that contains `pve_host` |

The command reads `pve_host` from the vars file with `bosh int <BOSH_VARS_STORE> --path=/pve_host`.

### Example

```bash
export BOSH_ENVIRONMENT=pve
export BOSH_DEPLOYMENT=cf
export BOSH_VARS_STORE=~/ocfp/deployments/cf/vars.yml

ocfp pve unstick router/0
```

On success, the command prints the systemctl status line confirming `active` and
exits 0. On failure it prints the error and exits non-zero.

### WARNING — `OCFP_SSH_UNSAFE`

By default, the SSH connection to the PVE host enforces host-key verification
using the local `known_hosts` file. If the PVE host is not in `known_hosts`,
the command fails.

Setting `OCFP_SSH_UNSAFE=1` disables host-key checking:

```bash
OCFP_SSH_UNSAFE=1 ocfp pve unstick router/0
```

**Do not use `OCFP_SSH_UNSAFE=1` in production.** An attacker who can intercept
the SSH connection will receive the `qm guest exec` command and can run arbitrary
code inside the VM. Use this flag only in isolated lab environments where the
network path to the PVE host is fully trusted and the risk of a man-in-the-middle
attack is negligible.

---

## `make integration-pve`

Runs the PVE lifecycle integration test harness. The harness exercises a 16-step
BOSH lifecycle (create env, upload stemcell, deploy, create VM, get disk,
attach/detach disk, delete VM, delete disk, delete env) against a live PVE cluster
using a JSON-RPC CPI binary.

### Required environment variables

| Variable | Required | Description |
|----------|----------|-------------|
| `OCFP_PVE_HOST` | **yes** | PVE API endpoint, e.g. `https://pve.example.internal:8006` |
| `OCFP_PVE_AUTH_TOKEN` | yes | PVE API token ID in `user@realm!token-name` form |
| `OCFP_PVE_TOKEN_SECRET` | yes | PVE API token secret UUID |
| `OCFP_PVE_NODE` | yes | PVE node name, e.g. `pve` |
| `OCFP_PVE_NETWORK_BRIDGE` | yes | Linux bridge for test VMs, e.g. `vmbr0` |
| `OCFP_CPI_BIN` | optional | Path to the CPI binary; defaults to `./bin/cpi` |
| `OCFP_VM_STORAGE` | optional | PVE storage pool for VM disks; defaults to `local-lvm` |
| `OCFP_STEMCELL_STORAGE` | optional | PVE storage pool for stemcell ISOs; defaults to `local` |
| `NETWORK_TEST_MODE` | optional | `sdn` or `bridge` (default `bridge`) |

The `OCFP_PVE_HOST` guard is enforced by the Makefile target. If the variable is
absent, the target prints an error message with a hint and exits 2.

### Running the full suite

```bash
export OCFP_PVE_HOST=https://pve.lab.example.internal:8006
export OCFP_PVE_AUTH_TOKEN=root@pam!ocfp-test
export OCFP_PVE_TOKEN_SECRET=xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
export OCFP_PVE_NODE=pve
export OCFP_PVE_NETWORK_BRIDGE=vmbr0

make integration-pve
```

The harness runs for up to 30 minutes (`-timeout 30m`). It prints a step-by-step
log for each of the 16 lifecycle stages.

### Running only the lifecycle test

```bash
make integration-pve-lifecycle
```

Equivalent to `go test -tags=integration -run TestPVELifecycle_16Steps ./tests/integration/...`.

### Dry-run (no live PVE required)

```bash
make integration-pve-dry-run
```

Runs `TestDryRun`, which validates config loading and CPI client construction
without making real API calls. Does not require `OCFP_PVE_HOST`.

### Skip behavior

Any integration test that detects a missing required env variable skips cleanly
with a `t.Skip` message rather than failing. Running `go test ./...` without the
env set is safe — all integration tests are skipped, not failed.

### Recovery after a failed run

A failed lifecycle test may leave orphaned VMIDs on the PVE cluster. The harness
uses a `ResourceTracker` with `t.Cleanup` to delete all VMs and disks created
during the test, but a SIGKILL or network partition can interrupt cleanup.

To recover manually:

1. Identify orphaned VMs with `pvesh get /nodes/<node>/qemu --output-format=json`.
2. Check whether the VMID falls inside the test VMID range (default `100..5999`).
3. If the BOSH agent is wedged but the VM is alive, use [`ocfp pve unstick`](#ocfp-pve-unstick-instance) to restart it.
4. Delete any remaining test VMs with `qm stop <vmid> && qm destroy <vmid>`.

---

## See also

- [PVE provider configuration](../pve.md) — bloc config reference and auth setup
- [PVE robustness changes](../migrations/pve-robustness-changes.md) — breaking changes and migration steps
- [PVE robustness architecture](../architecture/pve-robustness.md) — package layout and probe flow
