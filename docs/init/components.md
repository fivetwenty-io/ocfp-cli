# Component Initialization

This page covers the `ocfp init` components other than the bastion, which are `aws`, `gcp`, `pve`, `pg`, `bosh`, `cf`, and `all`. The bastion has its own page, [Bastion Initialization](bastion.md), because its init provisions a whole host.

## Command Usage

```bash
ocfp --bloc <bloc-name> init <component> [flags]
```

The mode flags `--genesis`, `--ocfp`, `--config`, and `--vault-boot-unit` belong to the bastion, so init refuses them on every component on this page, `all` included.

## What Each Component Does

| Component | What a real run does |
|-----------|----------------------|
| `aws` | Makes the bloc the current bloc and writes the bloc's `mgmt`, `ocf`, and prometheus monitoring Genesis env files. |
| `gcp` | Writes the same three Genesis env files and then makes the bloc the current bloc. It refuses a bloc whose env files already exist. |
| `pve` | Makes the bloc the current bloc, writes the three Genesis env files, and writes the PVE ops files and runtime config for `mgmt` and `ocf`. It takes the datacenter from the config's region. |
| `pg` | Logs the PostgreSQL databases it plans. It doesn't create any of them yet. |
| `bosh` | Writes the BOSH manifest to `~/deployments/<bloc>/bosh.yml`, creates the director with `bosh create-env`, and writes `~/deployments/<bloc>/bosh-env.sh`. It needs the bloc's bootstrap state from `ocfp bootstrap`. |
| `cf` | Uploads the cf-deployment release and deploys Cloud Foundry with `bosh`, then logs in with `cf`, creates the default org and the development space, and targets them. It needs a DNS name for the bloc in the config. |
| `all` | Runs the bastion, `pg`, `bosh`, and `cf` in that order, and stops at the first one that fails. |

`aws`, `gcp`, and `pve` take the bloc's name from `--bloc` or from the `OCFP_BLOC` environment variable. They refuse a run without one, and they refuse a name that has anything but lowercase letters, digits, and dashes, that starts or ends with a dash, or that is shorter than two characters.

The env files that `aws`, `gcp`, and `pve` write go under the bloc's own data directory, in `deployments/<slot>/`. A bloc that has a vault in both the XDG data directory and the legacy `~/.ocfp` directory can't be settled, so each of the three refuses it until we remove one.

## The Prerequisite Check

Before any component starts, init checks that the bastion is reachable. It runs `ssh` to the bastion with the bloc's SSH key and checks the bastion's host key against our `~/.ssh/known_hosts`, recording the key on first contact. Every component on this page stops when the check fails, because each one needs a working bastion. For `bosh`, `cf`, and `all`, the prerequisite step then warns about any of `bosh`, `cf`, `credhub`, and `uaa` that isn't on our `PATH`, and it creates `~/deployments/<bloc>` when it's missing. `--skip-checks` skips the whole step.

## Dry Run

```bash
# See what BOSH init would do without changing anything
ocfp --bloc 520-aws-wayne init bosh --dry-run
```

We ask for a dry run with `--dry-run`, or with `dry_run: true` under `init:` in our config file. An explicit `--dry-run=false` on the command line wins over the config file, so we can still make a real run from a config that asks for dry runs. `ocfp bastion init` reads the same setting, with the same rule.

A dry run makes every check that the real run makes before it changes anything, in the same order, and it stops with the same error wherever the real run would stop. It logs each change as a `DRY RUN: Would ...` line instead of making it, so it doesn't run `bosh` or `cf`, write any file, or change the current bloc. Some failures show up only when a write happens, such as a directory that can't be created or written, or a lock on the CLI state file that can't be taken. A dry run can't meet those, so it logs a `DRY RUN: Did not ...` line for each directory it would write in and for the state file it would lock. It ends with a line that says the dry run of the component finished without changing anything.

Here are some of the refusals that a dry run repeats:

- `aws`, `gcp`, or `pve` without a bloc name, or with a malformed one

- `gcp` on a bloc whose Genesis env files already exist

- `aws`, `gcp`, or `pve` on a bloc with a vault in both data directories, or with a CLI state file that can't be read

- `bosh` without the bloc's bootstrap state, with bootstrap state that can't be read, or when the bloc's state directory can't be created

- `bosh` or `cf` with a compiled-release pin built for a different stemcell

- `bosh` or `cf` when `~/deployments/<bloc>` is missing or isn't a directory

- `bosh` or `cf` when a `bosh` or `cf` command that it runs isn't on our `PATH`

- `cf` when the bloc has no DNS name in the config, or has one that isn't valid

The prerequisite check runs in a dry run too, but only the parts of it that read our own files. It finds the bloc's SSH key and checks the SSH user, the bastion's IP, the key path, and the `known_hosts` path, so it refuses any of them that the real run would refuse. It stops short of the connection itself, so it never logs in to the bastion and never writes the bastion's host key to our `~/.ssh/known_hosts`, and it logs that it didn't check the connection or the host key. When our config doesn't name the bastion's IP, a real run probes the bastion at the address in the bloc's state, asks the provider when that fails, and records the address it finds. A dry run does none of that, so it logs that it checked none of the connection. The bastion component's own dry run still asks the provider for the address, as [Bastion Initialization](bastion.md#dry-run) describes, because that lookup only reads. The prerequisite step logs the deployment directory it would create instead of creating it, and the component's own checks then count that directory as present.

`ocfp init all --dry-run` runs the bastion's dry run first, which is described on the [Bastion Initialization](bastion.md#dry-run) page, and then the dry runs of `pg`, `bosh`, and `cf`. It stops at the first component that would fail, as the real run does, and otherwise ends with a line that says the dry run of all components finished without changing anything.
