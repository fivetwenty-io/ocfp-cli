# 7. Management Vault: Where the Secrets Come Home

The inception vault has carried us well, but it was always a bootstrap expedient. One process, on one disposable VM, has been holding every secret the bloc owns. This chapter ends that arrangement. We use the new mgmt director to deploy a real, three-node Vault across our availability zones. Then we migrate every secret into it with checksums and retire inception. This is the second gate in the arc, and nothing beyond the management zone deploys until it closes.

A word on names, because they matter to the commands rather than only to the prose. The lab runs the **openbao** kit, which deploys OpenBao, the open-source Vault lineage. Genesis addresses a deployment by its type, and the type is the directory name under `~/ocfp/deployments/`, which `ocfp init bastion` creates as `openbao`. So the deployment we address below is `openbao`, and `@<env>:vault` resolves to nothing unless the bloc was built with `secrets_backend: vault`, which is the opt-in for staying on HashiCorp Vault.

Everything the deployment serves is still Vault as far as the rest of the arc is concerned. The `safe` CLI, the kits, and the API are unchanged, so when a later chapter says "the management Vault" it means this deployment.

## Deploy, initialize, unseal

The deployment itself is our first taste of the pattern that carries the rest of the arc, where we address a Genesis kit by env and type and deploy it through a director:

```bash
g @ocfp-lab-wayne-mgmt:openbao deploy -F -y
```

The kit places one node in each workload zone: `10.108.20.5`, `10.108.24.5`, `10.108.28.5` from our chapter 1 reservations, so a single zone's loss never costs us the secrets store. Behind the scenes this is also the PVE CPI's first director-driven outing. Watch the `g @ocfp-lab-wayne-mgmt:bosh b task` output or the PVE task log and we will see three VMs materialize in the 200+ VMID range.

A new Vault boots sealed and uninitialized, so we run the kit's init addon:

```bash
g @ocfp-lab-wayne-mgmt:openbao do i
```

This is one of the few interactive moments in the whole arc, and rightly so, because it produces the unseal keys and root token, and a human decides where those go. We treat them with the weight they deserve. Anyone holding a quorum of unseal keys owns every secret in the bloc, and losing them locks even us out after the next reboot.

Then we point `safe` at the new cluster, which is a separate step and an easy one to skip:

```bash
g @ocfp-lab-wayne-mgmt:openbao do t
```

The target addon finds a node that answers, runs `safe target` against it, and names the target after the environment, so we end up with a target called `ocfp-lab-wayne-mgmt`. That name is not decoration. The migration below looks its destination up as `<bloc>-mgmt` and validates that the target exists before it moves anything, so without this step the migration stops at its first check.

**Verify**:

```bash
g @ocfp-lab-wayne-mgmt:openbao info
safe targets
```

The deployment reports up, initialized, and unsealed on all three nodes, and `safe targets` lists both the inception target and the new `ocfp-lab-wayne-mgmt` one.

**Rollback**: before migration, this deployment is disposable (the secrets still live in inception), so a botched deploy is simply fixed and re-run (`deploy -F -y` again), or deleted via the director and redeployed.

## A map of the secret tree

Before we move 800-odd secrets, it pays to know what they are. Three namespaces cover everything, and telling them apart turns Vault-reference errors from mysteries into typos:

- `secret/config/<bloc>/<scope>/...` What the **ocfp CLI** writes, where `<scope>` is `mgmt` or `ocf`. It holds the network topology (`net/subnets/*` with reserved IPs and available bands), the CPI connection settings (`cpi/pve`), the RustFS endpoint and credentials, and the FQDNs. Kits read it via `meta.ocfp.vault.config`, which already carries the scope, so a kit reference looks like `/net/subnets/ocfp-0/reserved-ips:bosh_ip` with no scope in it.

  The scope segment is the one part of this path people leave out, and it is the part that changes the answer. Chapter 1 explains why. The mgmt and ocf scopes deliberately give different addresses for the same subnet, so `secret/config/<bloc>/mgmt/net/subnets/ocfp-0/reserved-ips:bosh_ip` reads `10.108.20.4` while the `ocf` path one segment over reads `10.108.20.64`. Reading the wrong one gives a plausible address rather than an error.

- `secret/ocfp/<parts-of-env-name>/...` What **Genesis generates per environment** (certificates, passwords, SSH keys) at a base derived from the env name (`ocfp-lab-wayne-ocf` becomes `secret/ocfp/lab/wayne/ocf/...`). Kits read it via `meta.vault`.

- `secret/exodus/<env>/...` What **deployments publish about themselves** for others to consume. The mgmt director's URL and admin credentials land here, and chapter 8's env file points at exactly this path to let the mgmt director deploy the ocf one.

The recurring gotcha is reaching into `config` data with a `meta.vault` (env-derived) base or vice versa; when a `(( vault ... ))` reference fails, the first question is always "which of the three trees does this key actually live in?"

## The migration

One command moves the whole tree:

```bash
ocfp vault migrate
```

It walks every secret out of inception into the mgmt Vault, verifying each with checksums. Then it re-targets `safe` and re-points the deployment repos' secrets provider at the new address. Our validated run moved 814 of 814 secrets and re-pointed the repos at `https://10.108.20.5`.

That address is the first node in the cluster's list rather than a load balancer, and the kit takes it from `static_ips[0]`. All three nodes serve the same raft cluster, so any of them would answer, but the one `safe` ends up targeting is the `z1` node.

It asks for confirmation along the way, and one of those prompts deserves our full attention rather than a reflexive yes. Migration runs in five steps, and step four decommissions inception. That step does not archive or disable anything. It lists every path under `secret/<bloc>-inception`, deletes each one, deletes the root, and then removes the local vault's files. Everything inception held is gone the moment we confirm it.

So answer that prompt no the first time through. Declining leaves inception running and intact, which is exactly what we want while we run the three verifications below, because they are what tell us the copy actually landed. Come back and decommission once they pass.

**Verify**, in three ascending degrees of confidence:

```bash
# 1. The active target flipped, no longer -inception on loopback.
safe targets

# 2. Real secrets read back from the new Vault.
safe get secret/config/ocfp-lab-wayne/rustfs
safe tree secret/exodus/

# 3. Genesis resolves through it end to end.
g @ocfp-lab-wayne-mgmt:openbao info
```

**Rollback**: there is one, and only up to a point. The copy really is a copy, so until we confirm the decommission prompt, inception still holds everything, and re-running `ocfp vault migrate` after a partial failure is safe. Once that prompt is confirmed the rollback is gone with it, and the only route back is whatever backup we took beforehand.

When the checks are green we let inception go. From here on the bastion is stateless again, rebuildable at will with no secrets aboard, which was the design all along.

One env-file subtlety graduates with us, and it is worth recording because it bit the validated run. Exodus references in env files should be **relative**, as in `bosh_env: ocfp-lab-wayne-mgmt@/secret/exodus/`, so that they follow the current secrets provider. An absolute reference frozen to the inception address fails exactly here, after migration, when inception no longer answers.

**The gate is closed.** Secrets live in a replicated, zone-spread Vault that BOSH manages and (from chapter 11) SHIELD backs up. If Vault-reference errors ever appear downstream, the reflex is `safe targets` first, because a target quietly reverted to inception is the classic cause.

Now the ocf zone gets a director of its own in [8. Environment BOSH](08-env-bosh.md).
