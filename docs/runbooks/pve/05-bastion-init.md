# 5. Bastion Init: Moving In

The bastion exists; now we make it home. This chapter installs the toolchain every deploy will run on (Genesis, the kits, the deployment repos) and formally meets the inception vault, the temporary secrets store that carries us until chapter 7. By the end, everything we do happens *from* the bastion, and our workstation's only job is `ocfp ssh`.

## Why deploys run from the bastion

The answer is the network. The directors, the Vault nodes, and every VM BOSH creates live on the SDN, reachable only from inside it. The bastion is the one machine with a foot in both worlds (SDN address `10.108.16.3`, tailnet name `ocfp-lab-wayne-bastion`), so it becomes the operator's bench. Genesis runs there, `safe` targets vaults from there, and the deployment repos live there. It stays out of the data path entirely; if we lose it, we rebuild it in a minute and re-run this chapter.

## One command, a full workshop

```bash
ocfp init bastion --bloc ocfp-lab-wayne
```

Full initialization installs the OCFP CLI, Genesis (from our configured repo and branch, which is `v3.2.x-dev` in the lab), `yq` and friends, the Genesis kits, and clones the deployments repo from `deployments.url` into `~/ocfp/deployments/`. It also wires the Genesis configuration to a secrets provider, which brings us to the introduction this chapter owes.

**The inception vault** is a small Vault the CLI runs locally on the bastion, on a loopback port derived from the bloc name. The derivation puts it somewhere in 18234 to 19233, and `ocfp-lab-wayne` lands on 18298, so that is the number this chapter quotes. A different bloc gets a different port on purpose, because that is what lets two of them run side by side without colliding. Port 8234 is the legacy value and applies only when no bloc is named at all, so we will not see it here.

We never need to compute the port ourselves. `safe targets` prints it, and `ocfp vault inception --bloc <bloc>` uses it. To pin one anyway, set `OCFP_VAULT_INCEPTION_PORT`.

A vault by that name made a cameo in chapter 4, when bootstrap needed somewhere to write the network topology and a CA to mint the artifacts certificate from. That one ran on our workstation. This command stands up a second one on the bastion, runs `ocfp vault populate` against it so it carries the same bloc facts, and then as its very last act stops the workstation copy and deletes that copy's `safe` target. The handoff happens inside this single command, and from here on the only inception vault that matters is the one on the bastion.

The workstation's vault data directory is left on disk deliberately, as a snapshot of the bootstrap-era secrets, so nothing is destroyed by the handoff. What goes away is the running process and the stale target that would otherwise shadow the real one. The name describes the design. We face a chicken-and-egg problem (secrets need a Vault, the real Vault needs a BOSH director, and the director needs secrets), and the inception vault is the egg.

It holds everything until the real management Vault exists in chapter 7, at which point `ocfp vault migrate` moves every secret across and this one retires. Loopback-only and bastion-local is exactly the right shape for something with that lifespan.

Re-runs get fast paths, and we will lean on them:

```bash
ocfp init bastion --bloc ocfp-lab-wayne --genesis   # Genesis + kits only
ocfp init bastion --bloc ocfp-lab-wayne --ocfp      # OCFP CLI binary only
ocfp init bastion --bloc ocfp-lab-wayne --config    # sync config files only
```

**Verify**: we log in and check the bench:

```bash
ocfp --bloc ocfp-lab-wayne ssh
```

Then, on the bastion:

```bash
g -v                    # Genesis alias active, reporting a v3.2.x-dev build
ocfp --version          # CLI present, version matching our workstation
ls ocfp/deployments/    # bosh  openbao  cf  ...one directory per deployment type
safe targets            # ocfp-lab-wayne-inception (*) at http://127.0.0.1:18298
```

That last line is the load-bearing one. `safe` targeting the inception vault confirms the secrets plumbing end to end, and it is the "before" picture we will compare against when chapter 7 re-points everything at the real Vault.

**Rollback**: `ocfp init bastion` is idempotent; re-run it whole or via a fast path. In the worst case the bastion itself is disposable, so we tear it down, re-run the bastion phase of chapter 4, and then run this chapter again. Losing the bastion does not lose secrets *after* chapter 7; before then, the inception vault lives here, which is one more reason not to linger in the bootstrap state.

## Dev kits, when we are the ones developing them

A default init sources Genesis and the kits from their remote git homes, which is right for operators consuming released kits. If that is us, we skip to the end of this chapter. But when we are carrying local kit changes that have not shipped (as lab work often does), the bastion needs our working trees. The env files already point there, because `kit: {name: dev}` resolves a `dev` symlink in each deployment directory to `~/kits/<kit>-genesis-kit/`. We put our trees behind those symlinks:

```bash
# From the workstation. The parent dirs must exist first.
ocfp --bloc ocfp-lab-wayne ssh bastion \
  'mkdir -p ~/kits/cf-genesis-kit ~/kits/bosh-genesis-kit'

ocfp rsync --bloc ocfp-lab-wayne --compress --exclude .git \
  src/kits/cf/   bastion:kits/cf-genesis-kit/
ocfp rsync --bloc ocfp-lab-wayne --compress --exclude .git \
  src/kits/bosh/ bastion:kits/bosh-genesis-kit/

# The cf deployment dir ships its dev symlink; bosh's must be created.
ocfp --bloc ocfp-lab-wayne ssh bastion \
  'ln -sfn ~/kits/bosh-genesis-kit ~/ocfp/deployments/bosh/dev'
```

The word `bastion` in those two commands is not decoration. `ocfp ssh` reads its first non-flag argument as the host to connect to, and only the literal string `bastion` makes it look the bloc's bastion up; anything else it uses verbatim as a hostname. So `ocfp ssh -- mkdir -p ...` does not run `mkdir` on the bastion; it tries to SSH to a machine called `mkdir`. A `--` separator does not help, because cobra strips it before the command ever sees it.

Quote the whole remote command as one argument as well. It reaches the far side through `bash -lc`, so `~` still expands there, and the shell in front of us leaves it alone.

Local env-file changes ride the same rsync, with two exclusions that matter. We never overwrite the bastion's `.genesis/`, because it was just wired to the inception vault and our local copy is stale, and we never clobber the `dev` symlinks we just made.

```bash
ocfp rsync --bloc ocfp-lab-wayne --compress \
  --exclude .genesis --exclude dev --exclude .git \
  src/deployments/fivetwenty-ocfp/bosh/ bastion:ocfp/deployments/bosh/
ocfp rsync --bloc ocfp-lab-wayne --compress \
  --exclude .genesis --exclude dev --exclude .git \
  src/deployments/fivetwenty-ocfp/cf/   bastion:ocfp/deployments/cf/
```

**Verify**: on the bastion, `~/ocfp/deployments/bosh/dev/kit.yml` and `~/ocfp/deployments/cf/dev/kit.yml` both resolve, and a spot-check of a known local change (say, a file under `~/kits/bosh-genesis-kit/ocfp/pve/`) shows our version, not origin's.

## Settling in

Two habits earn their keep for the rest of the arc. The first is the tmux session, and we start it on the bastion:

```bash
# On the bastion.
ocfp tmux
tmux attach -t ocfp
```

It has to be the bastion. `ocfp tmux` builds the session on whichever machine we run it from, and it gives every window a working directory under `~/ocfp/deployments/`. Those directories exist only on the bastion, so the same command on the workstation fails on its first window and creates no session at all. What we get is one window per deployment type, so a slow CF deploy in one window never holds up watching the directors in another.

The second habit is a debugging reflex. Genesis writes a trace log after every command to `~/.genesis/mylogs/last-trace`, and when a hook fails somewhere in chapters 6 through 9, that file is where the real error lives.

The bench is set. Genesis is on the right branch, the kits are in place, the repos are cloned, and the inception vault holds our secrets. Time to build the first director: [6. Management BOSH](06-mgmt-bosh.md).
