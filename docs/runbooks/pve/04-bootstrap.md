# 4. Bootstrap: First Contact

Three chapters of planning end here. One command reads the bloc we wrote and makes it physical. The network topology lands in Vault, security groups appear on the cluster, and a keypair is minted. The bastion boots and joins our tailnet, and with one more flag the artifacts store comes up beside it. This is the chapter where the lab stops being a YAML file.

## Previewing before we commit

Bootstrap is idempotent, but we still look before we leap. The dry run prints the full plan (every resource, named and typed) without touching the cluster:

```bash
ocfp bootstrap --bloc ocfp-lab-wayne --dry-run --output json
```

We read the plan the way we would read a diff. Every resource should carry our bloc prefix (`ocfp-lab-wayne-bastion`, `ocfp-lab-wayne-sg-*`), the network figures should match chapter 1, and nothing should reference a pool or template we do not have. A surprise here costs seconds; the same surprise mid-run costs a teardown.

One note is specific to PVE. We never pass `--public-ips` to a PVE bloc, because there are no cloud-allocated public addresses here. Ingress is the Cloudflare tunnel and Tailscale, as planned. Asking for them anyway is harmless rather than fatal, since the provider declares no support and the step skips itself, but it tells us nothing and it reads as though we expected something.

## The main event

```bash
ocfp bootstrap --bloc ocfp-lab-wayne --yes
```

That one command runs the whole bootstrap, all eleven steps of it, and it is worth saying plainly because the chapter reads as though it stops at the bastion. It does not. With no selective flag, bootstrap runs everything the bloc config asks for, and our config asks for artifacts, so the blobstore and its buckets come up on this run too. The `--artifacts` section below is how we re-run that part on its own, not how we reach it the first time.

We watch it work, because the sequence teaches us the system. Before step one the CLI starts the local inception vault if nothing is listening, since bootstrap needs somewhere to write, and with artifacts TLS in `internal-ca` mode it also needs a CA to mint from. Chapter 5 gives that vault a proper introduction.

Then the network phase carves our `/20` into the four `/22`s and writes the topology into Vault under `secret/config/ocfp-lab-wayne/`, meaning the subnets, the availability zones, the reserved statics, and the available bands. It also creates a real SDN subnet for each `/22`, so every per-zone gateway the plan promised actually answers.

Then come the security groups, shaped by our `allowed_ingress_ips`. The SSH keypair follows, stored under `~/.local/share/ocfp/<bloc>/ssh/`. Then comes the Cloudflare tunnel, if the bloc names one. And then the bastion arrives, a clone of `ubuntu-resolute-bastion-template` placed at `10.108.16.3`, with its per-VM configuration delivered through SMBIOS. That clone is auto-provisioned from the catalog on this cluster's first-ever run, and it takes about thirty seconds on every run after. On first boot the bastion resizes its disk, installs its tooling, and joins the tailnet with the auth key from our config. Its data disk is attached as a step of its own, so a re-run re-attaches the disk even when the bastion already exists.

Then comes ingress DNS, and finally the artifacts VM and its buckets.

**Verify**: we run three probes, from the outside in.

```bash
# The VM exists and runs.
pmx pve qemu list --cluster -o plain | grep ocfp-lab-wayne

# It joined the tailnet under its MagicDNS name.
tailscale status | grep ocfp-lab-wayne-bastion

# And we can log in — the CLI resolves bastion_ip and our bootstrap key.
ocfp --bloc ocfp-lab-wayne ssh
```

A bastion we can SSH into over Tailscale is the gate out of this chapter's first half. If the VM runs but never appears on the tailnet, the usual suspect is the `iso_storage` content-type gap from chapter 3 (the silent cloud-init fallback that skips the Tailscale setup), followed by an expired or ACL-rejected auth key.

**Rollback**: bootstrap is safe to re-run, because it reconciles rather than duplicates. For a true restart:

```bash
# Preview first, always.
ocfp teardown --bloc ocfp-lab-wayne --nuke --force --dry-run --output json
ocfp teardown --bloc ocfp-lab-wayne --nuke --force --empty
```

`--nuke` sounds worse than it is. It tells teardown to find the bloc's resources by asking the cluster rather than by reading the state file, which is what we want here precisely because we no longer trust the state file. It still deletes only resources whose names belong to this bloc, so a second bloc on the same host is untouched. The `--dry-run` is not ceremony, though, since it is the only thing that shows us the list before it goes.

That distrust of the state file has a specific cause worth knowing. If the PVE host was reinstalled out from under an existing bloc, the state file under `~/.local/state/ocfp/<bloc>/` still records every old resource, and bootstrap politely skips "existing" bastions and subnets that no longer exist anywhere. When host reality and state disagree, either prune the phantom entries after backing the file up, or empty the state, before re-running bootstrap.

## The artifacts store

PVE ships no object storage, and BOSH and Cloud Foundry both want an S3-compatible blobstore. Our answer is a dedicated VM running RustFS, and the run above has already built it. This is how we rebuild it on its own, without touching anything else:

```bash
ocfp bootstrap --bloc ocfp-lab-wayne --artifacts --yes
```

Either way the work is the same. It clones `ubuntu-resolute-template` into `ocfp-lab-wayne-artifacts` at `10.108.16.11`, attaches the data disk from our config, and runs the provision script over SSH, jumping through the bastion, which is why the bastion has to exist first.

When it finishes we have RustFS answering S3 on port 9000 with a certificate from the bloc's own CA, and eight buckets:

| Bucket | What writes to it |
|---|---|
| `ocfp-lab-wayne-mgmt-bosh` | the mgmt director's blobstore |
| `ocfp-lab-wayne-ocf-bosh` | the ocf director's blobstore |
| `ocfp-lab-wayne-ocf-cf-droplets` | cloud controller droplets |
| `ocfp-lab-wayne-ocf-cf-packages` | cloud controller packages |
| `ocfp-lab-wayne-ocf-cf-buildpacks` | cloud controller buildpacks |
| `ocfp-lab-wayne-ocf-cf-resource-pool` | the cloud controller resource pool |
| `ocfp-lab-wayne-mgmt-shield` | SHIELD archives for the mgmt tier |
| `ocfp-lab-wayne-ocf-shield` | SHIELD archives for the ocf tier |

The roster lives in one place in the code, so the create path and `ocfp artifacts provision` cannot drift apart. SHIELD gets a bucket per tier rather than one shared archive, and the four CF buckets are created now rather than later, even though nothing writes to them until chapter 9.

The store's coordinates go to Vault as well. The endpoint and TLS facts land at `secret/ocfp/ocfp-lab-wayne/artifacts`, and the per-consumer S3 credentials under `secret/config/ocfp-lab-wayne/<zone>/bosh/blobstores/` and `.../<zone>/cf/blobstores/`.

**Verify**: the CLI carries its own health checks, so we use them:

```bash
ocfp artifacts status --bloc ocfp-lab-wayne
ocfp artifacts lookup --bloc ocfp-lab-wayne
```

Status should report the VM up and RustFS serving, and lookup should print the endpoint and the bucket list. The Vault write is best-effort, so if the artifacts entry is missing, `ocfp vault populate` backfills it.

**Rollback**: the artifacts phase re-runs cleanly on its own (`--artifacts --yes` again), without disturbing the bastion.

## What just became true

One chapter created a surprising amount of state, and every later chapter leans on it. We now have a four-subnet SDN topology recorded in Vault, a bastion on the tailnet at a stable name, an S3 blobstore with per-purpose buckets and verified TLS, security groups shaped by our ingress list, and a keypair on disk. All of it is tagged `managed-by=ocfp`, reproducible from the one YAML file, and removable with a single teardown.

The infrastructure is up. Now we move in and set up the workshop: [5. Bastion init](05-bastion-init.md).
