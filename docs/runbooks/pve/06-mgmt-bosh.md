# 6. Management BOSH — The Proto-Director

Everything so far was staging. This chapter creates the first piece of the
platform proper: the management BOSH director, the machine that will deploy
every other machine. It faces the same chicken-and-egg problem the inception
vault solved for secrets: BOSH deploys VMs, but no BOSH exists yet to deploy
*this* VM.

The answer is `bosh create-env`, which runs the whole deployment process
from the bastion's own CPU: compile, render, then call the PVE CPI directly
to build one VM. We call the result the proto-director, and it is the only
VM in the bloc that BOSH does not manage from above.

## The CPI, and the one manifest that names it

The PVE CPI — `bosh-pve-cpi-release` — is the translator between BOSH's
wishes ("create a VM", "attach this disk") and the Proxmox API, speaking
through the token we minted in chapter 2. The BOSH kit pins a published
release by default, so an env file that says nothing about the CPI deploys
`bosh-pve-cpi/0.1.0` straight from its GitHub release — nothing to ship, and
nothing to keep in sync.

Carrying a dev build is the exception, and it means shipping the tarball
ourselves:

```bash
scp ~/w/proxmox/bosh-pve-cpi-release/dev_releases/bosh-pve-cpi/bosh-pve-cpi-dev-<build>.tgz \
    ocfp-lab-wayne-bastion:/home/ubuntu/
```

The mgmt env file, `bosh/ocfp-lab-wayne-mgmt.yml`, then names it in three
params that must agree with each other:

```yaml
pve_cpi_release_url: file:///home/ubuntu/bosh-pve-cpi-dev-<build>.tgz
pve_cpi_release_version: 0+dev.<n>
pve_cpi_release_sha1: <sha1 of the tarball>
```

Pinning a different published release uses the same three params with an
`https://` URL. The classic failure here is a `pve_cpi_release_version` that
does not match the version embedded in the tarball, so we check the source of
truth rather than guessing: `tar -xzOf <tgz> release.MF | grep version`. The
older `pve_cpi_release_path` param is gone — `genesis check` fails and names
the replacement if an env file still carries it. If we edited the env file on
the workstation, we re-sync with
`ocfp init bastion --bloc ocfp-lab-wayne --config` before deploying.

One more pre-flight: the CPI's connection settings live in Vault, written
there by bootstrap. A quick look confirms them:

```bash
safe tree secret/config/ocfp-lab-wayne          # find the scope
safe get secret/config/ocfp-lab-wayne/<scope>/cpi/pve
```

Host, token, node, storage pools, and network bridge should all read back as
the values from chapters 2 and 3.

## Render, then deploy

Genesis renders the full create-env manifest without touching anything, and
we always look first:

```bash
g @ocfp-lab-wayne-mgmt:bosh manifest
```

We scan for three things: our CPI release params, a noble stemcell, and the
director's static IP `10.108.16.4` from our chapter 1 plan. Satisfied, we
commit:

```bash
g @ocfp-lab-wayne-mgmt:bosh deploy -F -y
```

Now we wait, and the wait has texture. Compilation runs first and dominates
the wall clock. Then the CPI builds the VM through the PVE API, visible in
the PVE task log, a reassuring thing to watch on a first run. Then the
agent comes up, jobs start, and create-env writes its state file next to
the manifest.

That state file is how future create-env runs update this director in
place instead of duplicating it. It is precious; the deployment repo
carries it.

**Verify**:

```bash
g @ocfp-lab-wayne-mgmt:bosh info      # director URL and credential locations
g @ocfp-lab-wayne-mgmt:bosh b env     # 'b' = raw bosh passthrough
```

`b env` returning the director's name, UUID, and version is the platform's
first heartbeat: it proves the VM runs at `10.108.16.4`, the director stack
inside it is healthy, and the admin credentials Genesis wrote to Vault
actually work. In PVE, `pmx pve qemu list` (natively `qm list` on the
host) shows the director VM with a VMID from 200 up (the CPI's range,
above the hand-managed band).

**Rollback**: `bosh create-env` is convergent. A failed run is diagnosed
(the trace at `~/.genesis/mylogs/last-trace`, or the create-env output
itself) and simply re-run; with the state file intact it resumes rather than
duplicates. To remove the director entirely, `bosh delete-env` with the same
manifest and state file.

## Stocking the shelves

A director without stemcells can deploy nothing, so we finish by uploading
the OS images every later chapter draws from. One pin matters enough to
explain: the bosh kit's compiled releases are built against **noble 1.364**,
so the director VMs we deploy in chapter 8 must run exactly that version or
BOSH will reject the compiled packages. bosh.io also delists old
point-releases, so the download that always works is the GCS bucket:

```bash
# On the bastion, targeting the mgmt director.
bosh -n upload-stemcell \
  https://storage.googleapis.com/bosh-core-stemcells/1.364/bosh-stemcell-1.364-openstack-kvm-ubuntu-noble.tgz \
  --sha1 d6cc58bda0120fe47787a46775ff5bafc5718257
```

(The name carries no `-go_agent` suffix, and yes, `openstack-kvm` is
correct: the PVE CPI consumes OpenStack KVM stemcells.) Workload VMs are
free to ride newer nobles; the 1.364 pin exists for the *director*
deployments. If we ever bump it, the kit's compiled-release URLs bump in
lockstep.

**Verify**: `bosh stemcells` lists the noble stemcell on the mgmt director.

The proto-director is alive and provisioned. Its first assignment is to give
our secrets a permanent home: [7. Management Vault](07-mgmt-vault.md).
