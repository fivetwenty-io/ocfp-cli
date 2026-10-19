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
