# 7. Management Vault — The Secrets Come Home

The inception vault has carried us well, but it was always a bootstrap
expedient: one process, on one disposable VM, holding every secret the bloc
owns. This chapter ends that arrangement. We use the new mgmt director to
deploy a real, three-node Vault across our availability zones. Then we
migrate every secret into it with checksums and retire inception. This is
the second gate in the arc: nothing beyond the management zone deploys
until it closes.

A word on names: the lab's direction is the **openbao** kit (v1.1.0), which
deploys OpenBao, the open-source Vault lineage. It still provides the
`vault` deployment type, though, so every command below reads the same
either way. The validated run behind these runbooks used the dev vault
kit; when the openbao kit lands, this paragraph is the only thing that
changes.

## Deploy, initialize, unseal

The deployment itself is our first taste of the pattern that carries the
rest of the arc (a Genesis kit, addressed by env and type, deployed through
a director):

```bash
g @ocfp-lab-wayne-mgmt:vault deploy -F -y
```

The kit places one node in each workload zone: `10.108.20.5`, `10.108.24.5`,
`10.108.28.5` from our chapter 1 reservations, so a single zone's loss never
costs us the secrets store. Behind the scenes this is also the PVE CPI's
first director-driven outing: watch `bosh task` output or the PVE task log
and we will see three VMs materialize in the 200+ VMID range.

A new Vault boots sealed and uninitialized, so we run the kit's init addon:

```bash
g @ocfp-lab-wayne-mgmt:vault do i
```

This is one of the few interactive moments in the whole arc, and rightly so:
it produces the unseal keys and root token, and a human decides where those
go. We treat them with the weight they deserve — anyone holding a quorum of
unseal keys owns every secret in the bloc, and losing them locks even us out
after the next reboot.

**Verify**:

```bash
g @ocfp-lab-wayne-mgmt:vault info
```

The deployment reports up, initialized, and unsealed on all three nodes.

**Rollback**: before migration, this deployment is disposable (the secrets
still live in inception), so a botched deploy is simply fixed and re-run
(`deploy -F -y` again), or deleted via the director and redeployed.

## A map of the secret tree

Before we move 800-odd secrets, it pays to know what they are. Three
namespaces cover everything, and telling them apart turns Vault-reference
errors from mysteries into typos:

- `secret/config/<bloc>/...`
  What the **ocfp CLI** writes: network topology (`net/subnets/*` with
  reserved IPs and available bands), CPI connection settings (`cpi/pve`),
  RustFS endpoint and credentials, FQDNs. Kits read it via
  `meta.ocfp.vault.config`.

- `secret/ocfp/<parts-of-env-name>/...`
  What **Genesis generates per environment** (certificates, passwords, SSH
  keys) at a base derived from the env name (`ocfp-lab-wayne-ocf` becomes
  `secret/ocfp/lab/wayne/ocf/...`). Kits read it via `meta.vault`.

- `secret/exodus/<env>/...`
  What **deployments publish about themselves** for others to consume — the
  mgmt director's URL and admin credentials land here, and chapter 8's env
  file points at exactly this path to let the mgmt director deploy the ocf
  one.

The recurring gotcha is reaching into `config` data with a `meta.vault`
(env-derived) base or vice versa; when a `(( vault ... ))` reference fails,
the first question is always "which of the three trees does this key
actually live in?"

## The migration

One command moves the whole tree:

```bash
ocfp vault migrate
```

It walks every secret out of inception into the mgmt Vault, verifying each
with checksums. Then it re-targets `safe` and re-points the deployment
repos' secrets provider at the new address. Our validated run moved 814 of
814 secrets and re-pointed the repos at `https://10.108.24.5`.

It asks for confirmation along the way: this is the one command in the
chapter that changes where truth lives, and it does not do that silently.

**Verify**, in three ascending degrees of confidence:

```bash
# 1. The active target flipped — no longer -inception at 127.0.0.1:8234.
safe targets

# 2. Real secrets read back from the new Vault.
safe get secret/config/ocfp-lab-wayne/rustfs
safe tree secret/exodus/

# 3. Genesis resolves through it end to end.
g @ocfp-lab-wayne-mgmt:vault info
```

**Rollback**: migration copies rather than moves, so inception still holds
everything until we decommission it; re-running `ocfp vault migrate` after
a partial failure is safe.

When the checks are green we let inception go. From here on, the bastion is
stateless again (rebuildable at will, no secrets aboard), which was the
design all along.

One env-file subtlety graduates with us, worth recording because it bit the
validated run: exodus references in env files should be **relative**:
`bosh_env: ocfp-lab-wayne-mgmt@/secret/exodus/`, so they follow the current
secrets provider. An absolute reference frozen to the inception address
fails exactly here, after migration, when inception no longer answers.

**The gate is closed.** Secrets live in a replicated, zone-spread Vault that
BOSH manages and (from chapter 11) SHIELD backs up. If Vault-reference
errors ever appear downstream, the reflex is `safe targets` first — a target
quietly reverted to inception is the classic cause.

Now the ocf zone gets a director of its own:
[8. Environment BOSH](08-env-bosh.md).
