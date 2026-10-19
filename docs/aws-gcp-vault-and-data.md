# AWS and GCP Vault exports and bastion data

AWS and GCP bastions enable a 64 GiB persistent data disk by default. AWS uses
`gp3`; GCP uses `pd-balanced`. Both use ext4 at `/data`, retaining the operator
home, SSH host keys and service identity through guest reboots. An explicitly
configured storage type is preserved; `bastion.data.enabled: false` disables
the disk. Native cloud replacement/recycle remains a separate lifecycle
capability and is not enabled by these data defaults.

```yaml
bastion:
  data:
    enabled: true
    diskSizeGiB: 64
    storagePool: gp3 # use pd-balanced for GCP
    filesystem: ext4
    mountpoint: /data
```

Vault population reads bootstrap subnet IDs, network IDs, zones and security
identities. It stops before exporting when bootstrap state is unreadable or
malformed. Missing state is permitted for explicitly configured resources;
missing resources do not receive invented provider IDs or database endpoints.

AWS/GCP support scoped `vault populate reserved-ips` and `vault populate fqdns`
with the existing dry-run, filter, drift-preservation and force semantics.
AWS exports the relocated bastion address rather than the provider-reserved
legacy +3 slot. Dynamic pools exclude the final usable address reserved for
that bastion and GCP's provider-reserved second-last subnet address. Existing
observed bastion addresses are honored; overlaps with known static service
addresses fail instead of silently changing another service's address.

`init gcp` writes management BOSH, OCF CF and monitoring environment files,
refusing existing destination files. GCP CPI credentials may be inline JSON
or a JSON file; Vault receives validated contents. A region remains a region
and a configured zone remains a zone.

The checked-out GCP Genesis BOSH kit still consumes legacy paths and requires
a compatible kit revision before deployment. CLI tests establish the export
contract only; real cloud bootstrap, reboot, disk recovery and kit deployment
acceptance are deferred to the later live-test phase.

Cloud bootstrap and artifacts installation run as retryable systemd services.
The cloud-init payload schedules them without blocking its own completion.
RustFS requires its data mount before starting; bastion services require the
configured dataset before reading persisted identities. Existing filesystems
must carry their OCFP ownership label (`ocfp-data` or `ocfp-artdata`); an attached
foreign filesystem or failed disk probe is refused rather than adopted.
These installers target Ubuntu/systemd images with amd64 release binaries.

## Existing private egress

AWS and GCP default to public addresses on the bastion and artifacts VM.
For an operator-managed network with existing outbound access, configure:

```yaml
network:
  cloudEgress: existing-private
  id: existing-network-id
  strategy: wide
  subnets:
  - id: existing-subnet-id
    name: ocfp-0
    cidr: 10.0.4.0/22
    availabilityZone: us-central1-b
```

List the workload subnets in allocation order (type `ocfp`, the default).
Use actual provider identifiers and CIDRs. AWS subnet availability zones must
match the provider; GCP subnets are regional, so select the physical VM zone.
Bootstrap verifies all imported subnets belong to the configured network before
recording them. It never creates missing imported networks/subnets or configures
NAT. Existing egress must reach package/image endpoints and configured services.
Bastion and artifacts requests omit public addresses. Imported networks/subnets
are preserved by teardown, including provider discovery under another name.

The matching BOSH kit integration is maintained on `ocfp-gcp` from `develop`
in `work/kits/bosh/ocfp-gcp`; see that repository's `plans/ocfp-gcp.md`.


For GCP Shared VPC, set `enable_shared_vpc: true` and `host_project_id` to the
host project while `project_id` remains the VM/service project. Full imported
VPC and subnet handles must agree with that explicit network project. The
canonical Vault `net:project` feeds the BOSH kit's `xpn_host_project_id`.

## Native bastion OS refresh

AWS and GCP `ocfp bastion recycle --bloc NAME` replace the boot disk on the same VM. The instance, network interfaces, private addresses, and preserved data/home disk remain in place. The VM stops during the swap; an ephemeral public IP can change when it restarts. The configured image supplies the new root. A configured flavor change is rejected: resize the instance separately before recycling.

```sh
# Prepare and verify the new OS, keeping the original root for rollback.
ocfp bastion recycle --bloc NAME --stop-before-retire

# Finish tooling and Vault service initialization on the new OS.
ocfp bastion init --bloc NAME

# Resume and retire the original root after verification.
ocfp bastion recycle --bloc NAME

# Alternatively, restore the original root before retirement begins.
ocfp bastion recycle --bloc NAME --abort
```

The command verifies the guest boot payload, initial bootstrap packages, and the actual preserved data/home mounts before retiring the old root. This does not replace `bastion init`; run that command after a completed refresh too if it was not run while paused. Abort restores the original OS and startup metadata, but it does not rewind changes made to the shared data disk. After retirement starts, use the retained native snapshot for recovery. `--archive-storage` is specific to PVE; cloud refresh always takes a provider snapshot.

Interrupted runs resume from the bloc's journal. Provider identities and disk generations are checked before destructive actions; an ambiguous attachment or unreadable journal stops progress. A bloc lock prevents concurrent recycle runs. The journal contains original and replacement startup data, which may include credentials: state and backups must remain owner-only. Completed recovery records retain snapshot/root identifiers without retaining the active startup-data journal.

AWS root-volume creation uses an EC2 client token. Its snapshot API has no equivalent server token: the CLI finds an existing safety snapshot by the exact run token and source volume, and rejects duplicate matches. An uncertain create response followed by an eventually consistent empty lookup can leave an extra safety snapshot; it does not authorize deletion of the original root. Native snapshots are retained for operator-managed recovery and cleanup. Live cloud qualification remains a separate step.
