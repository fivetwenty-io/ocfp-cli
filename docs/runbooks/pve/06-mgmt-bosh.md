# 6. Management BOSH: The Proto-Director

Everything so far was staging. This chapter creates the first piece of the platform proper. The management BOSH director is the machine that will deploy every other machine, and it faces the same chicken-and-egg problem the inception vault solved for secrets. BOSH deploys VMs, but no BOSH exists yet to deploy *this* VM.

The answer is `bosh create-env`, which runs the whole deployment process from the bastion's own CPU. It compiles, renders, and then calls the PVE CPI directly to build one VM. We call the result the proto-director, and it is the only VM in the bloc that BOSH does not manage from above.

## The CPI, and how we pin it

The Proxmox CPI, `bosh-proxmox-cpi-release`, is the translator between BOSH's wishes ("create a VM", "attach this disk") and the Proxmox API, speaking through the token we minted in chapter 2. The BOSH kit pins a published release by default. An env file that says nothing about the CPI deploys `bosh-proxmox-cpi/0.7.0` straight from its GitHub release, so there is nothing to ship and nothing to keep in sync.

The release was called `bosh-pve-cpi` through 0.4.0 and was renamed to `bosh-proxmox-cpi` at 0.5.0, repository and tarball together. The job inside it is still `pve_cpi`, so cpi-config entries and property paths are unchanged. What did change is the name BOSH reads out of `release.MF`, and the kit now declares the new one. An env file that overrides the URL to point at a pre-0.5.0 tarball therefore fails at create-env with a name mismatch, so move any such pin to 0.5.0 or later.

Pinning a release by hand uses three params that must agree with each other, written into the mgmt env file at `bosh/ocfp-lab-wayne-mgmt.yml`. The current default is 0.7.0:

```yaml
pve_cpi_release_url: https://github.com/fivetwenty-io/bosh-proxmox-cpi-release/releases/download/v0.7.0/bosh-proxmox-cpi-0.7.0.tgz
pve_cpi_release_version: 0.7.0
pve_cpi_release_sha1: 78247db6829e51b4e91b5202ee75c444a272f9dd
```

Those are the kit's own defaults, so pinning 0.7.0 by hand only restates what the kit already does. Read the current values out of the kit rather than out of this page, because they move. They live in `ocfp/pve/base.yml` as `pve_cpi_release_version`, `pve_cpi_release_sha1`, and `pve_cpi_release_url`. The reason to write the block out is to pin a release other than the current default, and then the three values have to come from the same tarball or create-env rejects it.

Watch where the checksum comes from. The param is named `pve_cpi_release_sha1`, but BOSH reads the algorithm off the front of the value, so a `sha256:`-prefixed digest is accepted in the same field. The release page prints its `sha1:` line carrying exactly that, while the kit uses a real sha1 instead. Either works on its own, and mixing them does not, so take all three values from one source. `shasum -a 1 <tgz>` settles it if we are unsure.

Carrying a dev build is the exception to all of that, and it means shipping the tarball ourselves. This is one of two steps in the chapter that run from the workstation rather than the bastion, so we use `ocfp scp` and let it find the bloc's key for us:

```bash
# From the workstation.
ocfp scp --bloc ocfp-lab-wayne \
  ~/w/proxmox/bosh-proxmox-cpi-release/dev_releases/bosh-proxmox-cpi/bosh-proxmox-cpi-dev-<build>.tgz \
  bastion:/home/ubuntu/
```

A bare `scp` to the bastion's tailnet name usually fails here, and the reason is the key rather than the host. `ocfp scp` resolves `bastion:` from the bloc config and reaches for `~/.local/share/ocfp/<bloc>/ssh/id_ed25519`, which is not an identity that plain `scp` offers unless we have wired it into our own SSH config.

The same three params then name the local file instead of a release URL:

```yaml
pve_cpi_release_url: file:///home/ubuntu/bosh-proxmox-cpi-dev-<build>.tgz
pve_cpi_release_version: 0+dev.<n>
pve_cpi_release_sha1: <sha1 of the tarball>
```

The classic failure here is a `pve_cpi_release_version` that does not match the version embedded in the tarball, so we check the source of truth rather than guessing with `tar -xzOf <tgz> release.MF | grep version`. One older param is worth knowing about too. `pve_cpi_release_path` is gone, and `genesis check` fails and names the replacement if an env file still carries it.

## Before we deploy

Two things have to be true before `create-env` will do the right thing, and both are easy to assume.

The first is that the bastion is holding the env file we think it is. If we edited it on the workstation, we push it up with `ocfp rsync`, and this is the second of the chapter's two workstation steps:

```bash
# From the workstation, then back to the bastion to deploy.
ocfp rsync --bloc ocfp-lab-wayne --compress \
  --exclude .genesis --exclude dev --exclude .git \
  src/deployments/fivetwenty-ocfp/bosh/ bastion:ocfp/deployments/bosh/
```

`ocfp init bastion --config` will not do this job, and it is worth knowing why, because it fails quietly. That mode syncs three things, our OCFP config file, our SSH keys, and the bootstrap state file, and it never touches `~/ocfp/deployments/`. It reports success either way, so a deploy that follows it renders the bastion's older copy of the env file and gives us no hint that it did. The two exclusions are the ones chapter 5 explains. The bastion's `.genesis/` was wired to the inception vault during init and our local copy is stale, and the `dev` symlinks have to survive the transfer.

The second is the CPI's connection settings, which live in Vault, where `ocfp vault populate` writes them, once during bootstrap and again during bastion init. It writes the same record into both config scopes, so either path reads back the same values:

```bash
safe get secret/config/ocfp-lab-wayne/mgmt/cpi/pve
```

The host, the token, the node, the storage pools, and the network bridge should all read back as the values from chapters 2 and 3. Read the `mgmt` copy by habit, because that is the one the bastion's own tooling reads when it builds its `pmx` context, so if the two ever disagree, `mgmt` is the copy whose drift we would feel first.

## Render, then deploy

Genesis renders the full create-env manifest without touching anything, and we always look first:

```bash
g @ocfp-lab-wayne-mgmt:bosh manifest
```

We scan for three things: our CPI release params, a noble stemcell, and the director's static IP `10.108.16.4` from our chapter 1 plan. Satisfied, we commit:

```bash
g @ocfp-lab-wayne-mgmt:bosh deploy -F -y
```

Now we wait, and the wait has texture. Compilation runs first and dominates the wall clock. Then the CPI builds the VM through the PVE API, which is visible in the PVE task log and a reassuring thing to watch on a first run. Then the agent comes up, jobs start, and create-env writes its state file next to the manifest.

That state file is how future create-env runs update this director in place instead of duplicating it. It is precious; the deployment repo carries it.

**Verify**:

```bash
g @ocfp-lab-wayne-mgmt:bosh info      # director URL and credential locations
g @ocfp-lab-wayne-mgmt:bosh b env     # 'b' = raw bosh passthrough
```

`b env` returning the director's name, UUID, and version is the platform's first heartbeat, because it proves the VM runs at `10.108.16.4`, the director stack inside it is healthy, and the admin credentials Genesis wrote to Vault actually work. In PVE, `pmx pve qemu list --cluster` shows the director VM with a VMID from 200 up, which is the CPI's range, sitting above the hand-managed band.

That floor is the one chapter 1 promised to explain, and this is the director that carries it. The kit writes `vmid_range_start: 200` as a literal into these CPI properties, and its own comment says why it refuses to read the value from Vault. `safe` stores every leaf as a string, and the CPI's Go config decoder rejects a string where it wants an int. So `vmid_range_start` in the bloc config decides only what `ocfp` records in Vault, and the director never reads it. Moving the floor means overriding `cpi.pve_vmid_range_start` through the `bosh-configs` path instead.

**Rollback**: `bosh create-env` is convergent. We diagnose a failed run from the trace at `~/.genesis/mylogs/last-trace`, or from the create-env output itself, and then simply re-run it. With the state file intact it resumes rather than duplicates. To remove the director entirely, we run `bosh delete-env` with the same manifest and state file.

## Stocking the shelves

A director without stemcells can deploy nothing, so we finish by uploading the OS image every later chapter draws from. We run one noble line across the whole fleet, and the mgmt env file's `stemcell_url` and `stemcell_sha1` pair is the source of truth for which version that is (1.562 as we write this). Upload the same version here:

```bash
# On the bastion.
g @ocfp-lab-wayne-mgmt:bosh b upload-stemcell -n \
  --sha1 2c1715b4926ff895e779e1eaa738621887bcef1e \
  "https://bosh.io/d/stemcells/bosh-openstack-kvm-ubuntu-noble?v=1.562"
```

A bare `bosh` would have nothing to target here. Genesis creates no `bosh` alias for us, so we reach each director through its environment. The mgmt director is born by `create-env` and has no parent director above it, so `b` already points at the right place and needs no `--self`.

Note where `-n` sits. Genesis parses the options ahead of the bosh subcommand as its own and passes through only what it does not recognise, so a flag it later claims would be silently eaten rather than rejected. Genesis already uses `-n` for `--dry-run` on `deploy`. Putting `-n` after `upload-stemcell` takes the question away, because Genesis stops parsing at the first non-option argument.

(The name carries no `-go_agent` suffix, and yes, `openstack-kvm` is correct, because the PVE CPI consumes OpenStack KVM stemcells.) bosh.io delists old point-releases, so if that URL ever returns 404 the same tarball lives permanently at `https://storage.googleapis.com/bosh-core-stemcells/1.562/bosh-stemcell-1.562-openstack-kvm-ubuntu-noble.tgz`. Compiled releases cached for an older noble still apply, because the director reuses compiled packages across any noble 1.x stemcell. When we bump the line, we bump every env file, the CLI's `DefaultStemcell`, and this chapter together.

**Verify**: `g @ocfp-lab-wayne-mgmt:bosh b stemcells` lists the noble stemcell.

The proto-director is alive and provisioned. Its first assignment is to give our secrets a permanent home: [7. Management Vault](07-mgmt-vault.md).
