# 4. Bootstrap — First Contact

Three chapters of planning end here. One command reads the bloc we wrote
and makes it physical: the network topology lands in Vault, security groups
appear on the cluster, and a keypair is minted. The bastion boots and joins
our tailnet, and — with one more flag — the artifacts store comes up beside
it. This is the chapter where the lab stops being a YAML file.

## Previewing before we commit

Bootstrap is idempotent, but we still look before we leap. The dry run
prints the full plan (every resource, named and typed) without touching
the cluster:

```bash
ocfp bootstrap --bloc ocfp-lab-wayne --dry-run --output json
```

We read the plan the way we would read a diff. Every resource should carry
our bloc prefix (`ocfp-lab-wayne-bastion`, `ocfp-lab-wayne-sg-*`), the
network figures should match chapter 1, and nothing should reference a pool
or template we do not have. A surprise here costs seconds; the same surprise
mid-run costs a teardown.

One PVE-specific note: we never pass `--public-ips` to a PVE bloc. There are
no cloud-allocated public addresses here; ingress is the Cloudflare tunnel
and Tailscale, as planned. Asking for them only earns an error.

## The main event

```bash
ocfp bootstrap --bloc ocfp-lab-wayne --yes
```

Then we watch it work, because the sequence teaches us the system. First the
network phase carves our `/20` into the four `/22`s and writes the topology
(subnets, availability zones, reserved statics, available bands) into Vault
under `secret/config/ocfp-lab-wayne/`.

A small aside worth catching: this is the inception vault's debut. Bootstrap
needs somewhere to write, and with artifacts TLS in `internal-ca` mode it
also needs a CA to mint from, so the CLI quietly starts the local inception
vault if one is not already running. Chapter 5 gives it a proper
introduction.

Then security groups, matching our `allowed_ingress_ips`. Then the SSH
keypair, stored under `~/.ocfp/<bloc>/ssh/`. And finally the bastion: a
clone of `ubuntu-noble-bastion-template` (auto-provisioned from the catalog
on this cluster's first-ever run, about thirty seconds on every run after)
placed at `10.108.16.3` with VMID 100, its per-VM configuration delivered
through SMBIOS. On first boot it resizes its disk, installs its tooling, and
joins the tailnet with the auth key from our config.

**Verify**: three probes, from the outside in.

```bash
# The VM exists and runs (on the PVE host, or via the UI).
qm list | grep ocfp-lab-wayne

# It joined the tailnet under its MagicDNS name.
tailscale status | grep ocfp-lab-wayne-bastion

# And we can log in — the CLI resolves bastion_ip and our bootstrap key.
ocfp --bloc ocfp-lab-wayne ssh
```

A bastion we can SSH into over Tailscale is the gate out of this chapter's
first half. If the VM runs but never appears on the tailnet, the usual
suspect is the `iso_storage` content-type gap from chapter 3 (the silent
cloud-init fallback that skips the Tailscale setup), followed by an expired
or ACL-rejected auth key.

**Rollback**: bootstrap is safe to re-run — it reconciles rather than
duplicates. For a true restart:

```bash
ocfp teardown --bloc ocfp-lab-wayne --nuke --dry-run --output json   # preview first, always
ocfp teardown --bloc ocfp-lab-wayne --nuke --force --empty
```

## The artifacts store

PVE ships no object storage, and BOSH and Cloud Foundry both want an
S3-compatible blobstore. Our answer is a dedicated VM running RustFS, and it
gets its own bootstrap phase:

```bash
ocfp bootstrap --bloc ocfp-lab-wayne --artifacts --yes
```

This clones `ubuntu-noble-template` into `ocfp-lab-wayne-artifacts` at
`10.108.16.11`, attaches the data disk from our config, and runs the
provision script over SSH, jumping through the bastion — which is why the
bastion had to come first.

When it finishes we have RustFS answering S3 on port 9000 with a
certificate from the bloc's own CA, the standard buckets created
(`ocfp-lab-wayne-mgmt-bosh`, `ocfp-lab-wayne-ocf-bosh`,
`ocfp-lab-wayne-artifacts-blobstore`, `ocfp-lab-wayne-shield`, and the CF
buckets we will want later), and the endpoint plus credentials written to
Vault at `secret/config/ocfp-lab-wayne/rustfs`.

**Verify**: the CLI carries its own health checks, so we use them:

```bash
ocfp artifacts status --bloc ocfp-lab-wayne
ocfp artifacts lookup --bloc ocfp-lab-wayne
```

Status should report the VM up and RustFS serving; lookup should print the
endpoint and bucket list. If Vault is missing the `rustfs` entry (the write
is best-effort), `ocfp vault populate` backfills it.

**Rollback**: the artifacts phase re-runs cleanly on its own
(`--artifacts --yes` again), without disturbing the bastion.

## What just became true

One chapter created a surprising amount of state, and every later chapter
leans on it: a four-subnet SDN topology recorded in Vault, a bastion on the
tailnet at a stable name, an S3 blobstore with per-purpose buckets and
verified TLS, security groups shaped by our ingress list, and a keypair on
disk. All of it is tagged `managed-by=ocfp`, reproducible from the one YAML
file, and removable with a single teardown.

The infrastructure is up. Now we move in and set up the workshop:
[5. Bastion init](05-bastion-init.md).
