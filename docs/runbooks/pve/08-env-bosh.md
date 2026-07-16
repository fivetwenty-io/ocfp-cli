# 8. Environment BOSH — The ocf Zone Gets Its Director

The management zone is complete: a director, a Vault, an artifacts store.
Now the pattern that makes BOSH worth the ceremony pays off: instead of
another `create-env` slog, the mgmt director deploys the ocf zone's director
*as an ordinary deployment*. Two-tier by design: the mgmt tier runs the
machinery, the ocf tier runs Cloud Foundry, and each tier's director watches
the other tier's workloads without watching itself.

## How one director deploys another

The trick is in one line of the env file, `bosh/ocfp-lab-wayne-ocf.yml`:

```yaml
bosh_env: ocfp-lab-wayne-mgmt@/secret/exodus/
```

This tells Genesis to deploy this environment using the director whose
coordinates are published at `secret/exodus/ocfp-lab-wayne-mgmt/bosh`, the
mgmt director, via the exodus data it wrote about itself in chapter 6, read
from whatever Vault is currently active. Chapter 7's lesson applies
verbatim: the reference is *relative* (no vault address baked in), so it
followed us through the migration without editing.

Two more lines earn a pre-flight check, both hard-won findings from the
validated run:

- `scale: dev` — OCFP kits recurse into an out-of-memory loop during scaling
  lookups when `kit.scale` is unset. Every env file sets it.

- `genesis.bosh_exodus_base` — hand-crafted bosh env files must set this
  explicitly so the director's exodus data lands where dependents (like the
  vault env, and CF after it) expect to find it.

## Deploy

```bash
g @ocfp-lab-wayne-ocf:bosh deploy -F -y
```

Same kit as chapter 6, different delivery. This time nothing compiles on
the bastion: the mgmt director orchestrates, and the PVE CPI builds the VM.
The noble 1.364 stemcell we pinned in chapter 6 becomes the new director's
foundation — this is exactly the deployment that pin exists for. The
director comes up at `10.108.20.4`, the ocf zone's reserved static, with
CredHub alongside for the runtime secrets CF will generate.

**Verify**:

```bash
g @ocfp-lab-wayne-ocf:bosh info
g @ocfp-lab-wayne-ocf:bosh b env
```

Director name, UUID, version: the second heartbeat of the platform. Unlike
chapter 6 there is also a bird's-eye view now: on the *mgmt* director,
`bosh deployments` lists both `vault` and the ocf bosh deployment, our first
glimpse of the management tier doing its actual job.

**Rollback**: this is a normal BOSH deployment, so the normal machinery
applies: a failed deploy is diagnosed via `bosh task <id> --debug` on the
mgmt director (or `~/.genesis/mylogs/last-trace` for hook failures) and
re-run. The mgmt director's resurrector also watches this VM from here on:
if the ocf director VM dies, it gets rebuilt automatically.

## Stocking this director too

The ocf director deploys the workloads, so it needs the workload stemcell.
Workload VMs are not bound by the 1.364 director pin (any current noble
serves); the validated run used 1.460:

```bash
# On the bastion, targeting the ocf director.
bosh -e ocf upload-stemcell \
  https://storage.googleapis.com/bosh-core-stemcells/1.460/bosh-stemcell-1.460-openstack-kvm-ubuntu-noble.tgz
```

**Verify**: `bosh -e ocf stemcells` lists it.

## The cloud config we do not write

Readers who know BOSH will be waiting for the step where we hand-author a
cloud config (VM types, networks, AZs) and upload it. That step does not
exist here, and its absence is one of OCFP's better gifts. Under Genesis
3.2, the kits *generate* the cloud config from the network topology the
ocfp CLI wrote to Vault back in chapter 4: the subnets, the reserved
statics, the available bands, the AZ-to-node mapping. When we deploy CF in
the next chapter, its kit uploads a named config (`ocfp-lab-wayne-ocf.cf`)
automatically, diffing against the director's current one and prompting on
change.

So the rules of the road are:

- We never author `bosh/configs/cloud/*.yml`. Tuning happens in the env
  file's `bosh-configs.cloud.*` keys (for example,
  `networks.ocf.allocation.size`).

- We never hardcode `cf_*_network` names. The generated network is named
  `<env>.<type>.net-ocf` (`ocfp-lab-wayne-ocf.cf.net-ocf` for us), and the
  kit keeps the references consistent on its own.

- If workload VMs ever land on addresses that collide with infra statics,
  the fix is upstream: adjust the bloc config's available band and re-run
  `ocfp vault populate`, never a hand-edit of a generated config.

Do not be surprised that `bosh -e ocf configs` is still empty right now; the
named config appears with the first CF deploy, which is exactly where we are
headed: [9. Cloud Foundry](09-cloud-foundry.md).
