# 8. Environment BOSH: A Director for the ocf Zone

The management zone is complete, with a director, a Vault, and an artifacts store all in place. Now the pattern that makes BOSH worth the ceremony pays off. Instead of another `create-env` slog, the mgmt director deploys the ocf zone's director *as an ordinary deployment*. The design is two-tier by intent. The mgmt tier runs the machinery, the ocf tier runs Cloud Foundry, and each tier's director watches the other tier's workloads without watching itself.

## How one director deploys another

The trick is in one line of the env file, `bosh/ocfp-lab-wayne-ocf.yml`:

```yaml
bosh_env: ocfp-lab-wayne-mgmt@/secret/exodus/
```

This tells Genesis to deploy this environment using the director whose coordinates are published at `secret/exodus/ocfp-lab-wayne-mgmt/bosh`, the mgmt director, via the exodus data it wrote about itself in chapter 6, read from whatever Vault is currently active. Chapter 7's lesson applies verbatim. The reference is *relative*, with no vault address baked into it, so it followed us through the migration without any editing.

Two more lines earn a pre-flight check, and both are hard-won findings from the validated run:

- `scale: dev` is required, because OCFP kits recurse into an out-of-memory loop during scaling lookups when `kit.scale` is unset. Every env file sets it.

- `genesis.bosh_exodus_base` has to be set explicitly in hand-crafted bosh env files, so that the director's exodus data lands where its dependents, such as the vault env and CF after it, expect to find it.

## Deploy

```bash
g @ocfp-lab-wayne-ocf:bosh deploy -F -y
```

Same kit as chapter 6, different delivery. This time nothing compiles on the bastion, because the mgmt director orchestrates and the PVE CPI builds the VM. The noble 1.562 stemcell we uploaded in chapter 6 becomes the new director's foundation. The director comes up at `10.108.20.64`, the reserved `bosh` static of the ocf scope, with CredHub alongside for the runtime secrets CF will generate. Chapter 1 explains why that is `.64` and not `.4`, which is the same static one tier down.

**Verify**:

```bash
g @ocfp-lab-wayne-ocf:bosh info
g @ocfp-lab-wayne-ocf:bosh b --self env
```

The `--self` matters more than it looks. Without it, `b` talks to the director that *deploys* this environment, which here is the mgmt director at `10.108.16.4`, so a bare `b env` cheerfully reports the wrong UUID and we learn nothing about the director we just built. With it, `b` talks to the deployed director itself at `10.108.20.64`. The flag belongs to `bosh` environments only. A deployment environment such as `cf` has no director under it, so a bare `b` there already reaches the director that deploys it, and Genesis rejects `--self` on anything that is not a `bosh` environment.

The verify block gives us the director name, UUID, and version, which is the second heartbeat of the platform. Unlike chapter 6 there is a bird's-eye view now as well. `g @ocfp-lab-wayne-mgmt:bosh b deployments` asks the mgmt director what it is carrying, and it lists both the `openbao` deployment from chapter 7 and the ocf bosh deployment we just made. That is our first glimpse of the management tier doing its actual job.

**Rollback**: this is a normal BOSH deployment, so the normal machinery applies. We diagnose a failed deploy with `g @ocfp-lab-wayne-mgmt:bosh b task <id> --debug` on the mgmt director, or with `~/.genesis/mylogs/last-trace` when the failure is in a hook, and then we run it again. The mgmt director's resurrector also watches this VM from here on, so if the ocf director VM dies, it gets rebuilt automatically.

## Stocking this director too

The ocf director deploys the workloads, and they ride the same noble line as the directors, so upload the same version here (1.562 as we write this):

```bash
# On the bastion, targeting the ocf director itself.
g @ocfp-lab-wayne-ocf:bosh b --self -n upload-stemcell \
  --sha1 2c1715b4926ff895e779e1eaa738621887bcef1e \
  "https://bosh.io/d/stemcells/bosh-openstack-kvm-ubuntu-noble?v=1.562"
```

Genesis builds no `bosh` alias for us, so there is no `-e ocf` to target. We reach each director through its environment instead, and `b --self` is what picks the deployed director over its parent.

**Verify**: `g @ocfp-lab-wayne-ocf:bosh b --self stemcells` lists it.

## The cloud config we do not write

Readers who know BOSH will be waiting for the step where we hand-author a cloud config (VM types, networks, AZs) and upload it. That step does not exist here, and its absence is one of OCFP's better gifts. Under Genesis 3.2, the kits *generate* the cloud config from the network topology the ocfp CLI wrote to Vault back in chapter 4: the subnets, the reserved statics, the available bands, and the AZ-to-node mapping. When we deploy CF in the next chapter, its kit uploads a named config (`ocfp-lab-wayne-ocf.cf`) automatically, diffing against the director's current one and prompting on change.

So the rules of the road are:

- We never author `bosh/configs/cloud/*.yml`. Tuning happens in the env file's `bosh-configs.cloud.*` keys (for example, `networks.ocf.allocation.size`).

- We never hardcode `cf_*_network` names. The generated network is named `<env>.<type>.net-ocf` (`ocfp-lab-wayne-ocf.cf.net-ocf` for us), and the kit keeps the references consistent on its own.

- If workload VMs ever land on addresses that collide with infra statics, the fix is upstream rather than a hand-edit of a generated config. Note that the `available_ip_*` keys in the bloc config will not move the band once bootstrap state exists, because the band then comes from the reserved-IP strategy. Changing it means changing the strategy, or overriding the band through the env file's `bosh-configs.cloud.*` keys.

Do not be surprised that `g @ocfp-lab-wayne-ocf:bosh b --self configs` is still empty right now; the named config appears with the first CF deploy, which is exactly where we are headed in [9. Cloud Foundry](09-cloud-foundry.md).
