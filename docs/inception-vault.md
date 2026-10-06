# The Inception Vault

The inception vault is the small vault that holds a bloc's secrets while the bloc is being bootstrapped, before the bloc has a production vault of its own. ocfp runs it with `safe local` inside a tmux session, first on the operator's workstation during `ocfp bootstrap` and then on the bastion during `ocfp init bastion`. Since ocfp v0.3.9 the vault keeps its data in integrated raft storage, so a stopped vault can be reopened with its saved keys instead of being replaced.

This page explains where the vault keeps its files, what `ocfp vault inception` does with each state it finds on disk, which directories a migration or an archive leaves behind, and how we recover from each error the command can return.

## Requirements

ocfp v0.3.9 needs safe v1.25.0 or later, because that is the first safe release that can reopen a raft vault with a saved root token. An older safe fails the prerequisite check with an upgrade message before anything is stopped or moved. The engine is OpenBao or HashiCorp Vault, and ocfp finds it the same way safe does, honouring `SAFE_ENGINE` when it is set.

Migrating a file-backed vault from an older ocfp also needs an engine that can still read file storage and that has the `operator migrate` command. OpenBao 2.7 and earlier and HashiCorp Vault both qualify. When the engine on `PATH` cannot do it, the command says so and leaves the file-backed data where it is.

## Ports

The API port is derived from the bloc name and lands somewhere between 18234 and 19233, so several blocs can run an inception vault on one machine. Raft also needs a cluster port, and ocfp always uses the API port plus 1000, which puts it between 19234 and 20233. Both ports must be free when the vault starts. Port 8234 is the legacy API port, and ocfp uses it only when no bloc is named.

To move a bloc's vault, we set `OCFP_VAULT_INCEPTION_PORT`, or the port in the bloc's config, and the cluster port moves with it. An API port above 64535 leaves no room for its cluster port, so ocfp rejects it before doing any work.

## Files on disk

In the bloc layout, which every current bloc uses, the vault's files live under the data home, which is `~/.local/share/ocfp` unless `XDG_DATA_HOME` or `OCFP_HOME` says otherwise.

| Path | What it holds |
|------|---------------|
| `<bloc>/vault/data/` | The vault's storage. A raft vault has `vault.db` and a `raft/` directory here, and a file-backed vault from an older ocfp has a `core/` directory instead. |
| `<bloc>/vault/root.key` | The root token, with mode 0600. |
| `<bloc>/vault/unseal.keys` | The unseal key, with mode 0600. |
| `<bloc>/vault/root.key.saferc-<timestamp>` | A copy of the root token that safe held for the bloc's target, kept with mode 0600 when it differed from `root.key` at the moment ocfp stopped the vault. |
| `<bloc>/vault/root.key.rejected-<timestamp>` | A root token that the engine refused, moved aside after the token from safe's target opened the vault instead. |

ocfp writes every key file through a temporary file in the same directory. It flushes that file to disk before the file takes the key file's name, and it flushes the directory afterwards, so neither a crash nor a power loss can leave a key file empty or half-written.

Two more files live under the state home, which is `~/.local/state/ocfp` unless `XDG_STATE_HOME` or `OCFP_HOME` says otherwise.

| Path | What it holds |
|------|---------------|
| `<bloc>/logs/vault/vault-inception.log` | Everything `safe local` printed on its last start. The previous start's log is kept beside it with a `.previous` suffix, and each older log is kept as `.previous-<timestamp>`. ocfp never replaces or deletes any of them, because a log may hold the only copy of a new vault's unseal key. We treat every one of them like a key file. |
| `<bloc>/inception-vault.lock` | The lock that lets only one ocfp run work on the bloc's vault at a time. |

When no bloc is named, the vault keeps its data in `~/.vault`, the root token in `~/vault.root.key`, and the unseal key in `~/vault.unseal.keys`, each key file with mode 0600. Older releases wrote both keys to a single `~/vault.key`, where the second write replaced the first. ocfp no longer reads, writes, moves, or deletes that file. If it exists, it stays where it is, and we keep it until we know the vault it belonged to is no longer needed, because it may hold that vault's only copy of a key.

## What a run does

Each run of `ocfp vault inception` brings the bloc to a running raft vault and changes as little on disk as it can. Every check that can refuse runs before anything is stopped or moved, so a refusal always leaves the disk exactly as it was.

One of those checks reads both key files. A key file that exists but cannot be read, such as one that a `sudo ocfp` run left owned by root, says nothing about whether the key is good, so ocfp stops with an error that names the file. It never treats such a file as missing, and it never archives the vault or writes a recovered key over it. An empty file counts as missing whatever its mode.

```mermaid
flowchart TD
    A[Probe the API port] -->|something that is not a vault| X1[Refuse: port taken]
    A --> B[Read the migration journal]
    B -->|cannot be trusted| X2[Refuse: journal invalid]
    B --> C[Classify the data directory]
    C -->|both core/ and raft| X3[Refuse: mixed data]
    C --> D{Does a vault answer?}
    D -->|yes, but not this bloc's| X1
    D -->|yes, healthy raft, no journal| H[Leave it running]
    D -->|yes, other state| R[Recover any missing key while it runs]
    R -->|open vault, keys still not saved| X5[Refuse: keys not saved, the vault keeps running]
    D -->|no| T
    R --> T[Keep the root token from safe's target]
    T --> S[Stop the vault and wait until the port and data are free]
    S --> P{Cluster port free?}
    P -->|no| X4[Refuse: cluster port taken]
    P -->|yes| J{Journal present?}
    J -->|yes| M[Resume the migration]
    J -->|no| K{Data and keys}
    K -->|raft data, both keys| RS[Restart in place]
    K -->|file data, both keys| MG[Migrate to raft, then restart]
    K -->|no data, no keys| F[Start a new vault]
    K -->|anything else| AR[Archive, then start a new vault]
    RS -->|engine rejects root.key, safe's token differs| RT[Restart once with safe's token]
    RS -->|engine rejects the keys| AR
    RT -->|engine rejects it too| AR
```

A vault counts as this bloc's own when the process listening on the API port also holds this bloc's `vault.db`. Without raft data there is no lock to compare, and the bloc's tmux session alone proves nothing, because its shell outlives safe. So ocfp follows the listening process up through its parents, at most eight levels, and counts the vault as the bloc's own only when it reaches a pane of the bloc's tmux session. An engine that outlived its safe has been adopted by init and fails this check, so ocfp leaves it running and reports the port as taken. We stop such an engine by hand once we have confirmed it is the bloc's. Derived ports can collide between blocs, and this check is what keeps one bloc from stopping a sibling's vault.

A vault is healthy when it is initialized and unsealed, it reports raft storage or no storage type at all, the data directory holds raft data, and the bloc's tmux session exists. ocfp leaves a healthy vault running. If safe has lost the bloc's target, ocfp registers it again and authenticates it with the token in `root.key`.

An open vault, meaning one that is initialized and unsealed, also needs both of its keys saved, whether or not it is healthy, because without them it cannot be reopened once it stops. When `root.key` or `unseal.keys` is missing, blank, or not the shape of a key, ocfp tries to recover the key from the running vault. If both keys are still not saved after that, the command exits non-zero with the error that the keys were not saved. It keeps no token, and it stops, archives, migrates, and starts nothing, so the vault keeps running and its secrets can still be read through it. Running the command again gives the same error until the keys are in place, so a vault in this state is never reported as healthy and is never replaced. `ocfp init bastion` runs `ocfp vault inception` on the bastion every time, even when a vault there already answers, so the bastion gets the same check. When the command fails, the bastion init fails with its exit code.

A sealed or never-initialized vault holds nothing in memory that its data and key files do not, so stopping it loses nothing. Before ocfp stops such a vault, it tries to recover a missing key while the vault still runs, and when a key is still missing after the stop, the vault goes down the archive path with its data kept.

Key recovery works the same way for every running vault. The unseal key comes from the current start's log, then from the tmux pane's history, and then from the logs of earlier starts, newest first. A vault prints its key once, on the start that created it, so after a few restarts the key sits in an older log. A log that exists but cannot be read stops the run with an error. The root token comes from the bloc's own target in `~/.saferc`, and only when that target points at the bloc's port. ocfp writes only the key files that are missing or blank, and it never replaces a key file that holds a value or one that it cannot read.

Stopping a vault deletes the bloc's safe target, and that target may hold the only copy of the root token. So before every stop, whether the vault is running or not, ocfp reads the token that `~/.saferc` holds for the bloc's own target, and only when that target points at the bloc's port. When `root.key` is missing or blank and the token has the shape of a token, ocfp writes it to `root.key`. When `root.key` holds a different value, ocfp keeps both, and writes the token to `root.key.saferc-<timestamp>` beside `root.key` with mode 0600. That copy sits inside the bloc's `vault` directory, so an archive carries it along. A later run that finds the same token in an existing copy reuses that copy rather than writing another one. The same goes for a new vault that fails to start, because safe may already have initialized it and saved its token in the target before the failure. ocfp keeps that token in `root.key` before it stops the new vault, and when the token cannot be kept, it leaves the new vault running and says why.

A stopped vault has no running process to recover a key from, but its logs may still hold one. When the data directory holds raft or file data and `unseal.keys` is missing or blank, ocfp looks for a whole unseal key in `vault-inception.log`, then in `vault-inception.log.previous`, and then in each `vault-inception.log.previous-<timestamp>`, newest first, and it writes the key from the newest log that has one to `unseal.keys` with mode 0600. Only then does it decide whether the vault can be restarted or has to be archived. A log that exists but cannot be read stops the run with an error, and nothing is started or archived.

## The archive

ocfp archives a vault only when one of its keys is missing, when key files exist without any data, or when the engine itself rejects the saved root token or unseal key. It never stops an open vault whose keys are not saved, so a vault that is archived for a missing key was already stopped, sealed, or never initialized. Network trouble, a busy port, or an engine that fails to start never leads to an archive. In those cases ocfp stops whatever it started and returns an error.

When the engine refuses the token in `root.key` and ocfp kept a different token from safe's target before the stop, ocfp restarts the vault once more with that token before it archives anything. If the engine takes it, `root.key` is updated to hold it, the refused token moves to `root.key.rejected-<timestamp>`, and the vault keeps its data. If the engine refuses that token too, ocfp archives the vault with both tokens inside the archive. A retry that fails for any other reason returns an error and archives nothing.

An archive renames the bloc's whole `vault` directory, with its data and both key files, to `vault.superseded-<timestamp>` beside it. Nothing is deleted. A new vault then starts in an empty `vault` directory, and the command ends with an error-level log line that names the archive, so the change is hard to miss. In the layout without a bloc and in the test layout, where the key files sit in the home directory, the data directory and each key file are renamed aside under the same suffix. An old `~/vault.key` is never part of an archive and is left in place.

To read the old secrets, we start a throwaway vault from the archive with safe by hand, using the archived `root.key` and `unseal.keys`, on a scratch port.

## Migration from file storage

An inception vault created by ocfp v0.3.7 or earlier stores its data in file storage. When ocfp finds such a vault with both keys, it stops the vault and migrates it to raft with the engine's `operator migrate` command. The steps run in this order.

1. ocfp checks that the engine has `operator migrate`, and refuses before it writes anything if the engine does not.

2. It writes a journal, `data.raft-migration.json`, with the phase `migrating`, and creates an empty staging directory, `data.raft-migrating`, with mode 0700.

3. It renders a migrate config and runs `operator migrate` with a ten-minute limit. The config and the engine's output are kept in the log directory as `raft-migration-<timestamp>.hcl` and `raft-migration-<timestamp>.log`.

4. It sets the journal's phase to `swapping`, renames `data` to `data.file-backup-<timestamp>`, renames the staging directory to `data`, and removes the journal.

5. It restarts the vault on the raft data with the saved keys.

The file store stays in `data.file-backup-<timestamp>` after a successful migration, and ocfp never removes it. Once we are satisfied that the raft vault holds everything, we can delete the backup ourselves.

When the copy fails, ocfp moves the staging directory aside to `data.raft-failed-<timestamp>`, removes the journal, and leaves `data` exactly as it was. Because the data is still file-backed, the next run tries the migration again instead of starting an empty vault over unmigrated secrets. When the restart right after a migration fails, ocfp never archives, even if the engine rejects the keys. Those keys opened the file store moments earlier, so the error names both the raft data and the backup, and we can look at them ourselves.

### Resuming an interrupted migration

When ocfp finds a journal, it reads it before it classifies the data directory, because a swap cut short can leave `data` missing, and a missing `data` would otherwise look like an empty bloc.

A `migrating` journal means the copy never finished and never touched `data`. ocfp moves any staging directory aside to `data.raft-partial-<timestamp>`, removes the journal, and runs the migration again.

A `swapping` journal means the copy finished. ocfp recognises the three points at which a swap can stop, which are before either rename, after `data` became the backup, and after both renames. It completes whichever rename is missing, removes the journal, and restarts the vault. Any other arrangement of the directories is refused with every path named, and nothing is moved.

A journal that cannot be parsed, names an unknown phase, or points its backup anywhere other than a `data.file-backup-*` directory beside `data` is refused rather than ignored.

### Migrating by hand

When the automatic migration refuses because the engine cannot read file storage, we can migrate with an older engine by hand and let ocfp take over afterwards. We stop the vault first, with `tmux kill-session -t <bloc>-inception-vault`, and confirm that nothing listens on the API port any more.

Then we write a config like this one, with the bloc's own paths and its cluster port, and save it with mode 0600.

```hcl
storage_source "file" {
  path = "/home/me/.local/share/ocfp/<bloc>/vault/data"
}

storage_destination "raft" {
  path    = "/home/me/.local/share/ocfp/<bloc>/vault/data.raft-manual"
  node_id = "safe-local"
}

cluster_addr = "https://127.0.0.1:<cluster port>"
```

The node ID must be `safe-local`, because that is the ID safe gives its raft node, and a store migrated under any other ID will not open. We create the destination directory with mode 0700, run `<engine> operator migrate -config=<file>` with the older engine, and wait for it to report that all the keys were migrated. Then we rename `data` to `data.file-backup-manual` and `data.raft-manual` to `data`. The next `ocfp vault inception` finds raft data with both keys and restarts the vault in place.

## Stopping and teardown

ocfp stops a vault by killing the bloc's tmux session, the listener on the bloc's API port, and any `safe local` process for that port. It then waits up to fifteen seconds for the port to close and for no process to hold `vault.db`, and it sends SIGKILL to anything still holding the data after a grace period. If the data stays locked, ocfp returns an error and moves nothing, because archiving or migrating under a live engine is how a store gets corrupted.

`ocfp vault teardown` stops the bloc's vault the same way, deletes its safe target, and archives it as described above. Before it stops anything, it keeps the token from the bloc's safe target in `root.key`, or beside it when `root.key` holds something else, so the token goes into the archive with the data. It first checks that the vault on the port is the bloc's own, and it stops nothing when it cannot prove that. When `ocfp init bastion` hands the vault over to the bastion, it stops the workstation's vault, and before it stops anything or deletes the safe target, it keeps the token from that target the same way, in the workstation bloc's `root.key` or beside it. When the token cannot be kept, the handover stops nothing and deletes nothing, and says why. The stop includes an engine that outlived its safe but still holds the bloc's raft data. The workstation's data stays on disk as a snapshot of the bootstrap-era secrets.

## A cut-short unseal key

A bloc created by ocfp v0.3.7 may have an `unseal.keys` file that holds only part of the key. That release read the key from a tmux pane, and a pane cuts a long line at its edge, so the saved key came out short. An engine fed a short key refuses it, and the vault cannot be reopened.

Before it starts a stopped vault, ocfp now checks that `unseal.keys` holds a whole key, which is 64 hexadecimal characters. When it does not, ocfp looks for the whole key in the vault log, in the logs of earlier starts, and in the pane's history. If any of them still has the key, ocfp restores it to `unseal.keys` and carries on. If none does, ocfp stops with `ErrUnsealKeyFileMalformed` and leaves the disk exactly as it found it.

To recover by hand, we find the line in the vault log that reads `Your Vault Seal Key is` or `Your OpenBao Seal Key is`, and copy the 64 hexadecimal characters that follow. We write them to `unseal.keys` with mode 0600, and then we run the command again.

safe passes the engine only the first line of `unseal.keys`, with nothing but its line ending removed, so a whole key behind a blank line or next to a stray space would reach the engine as a key it refuses. When a key file holds a whole key with extra whitespace around it, ocfp rewrites the file to hold just the key and a newline, at mode 0600, before it starts anything. It does the same for `root.key` when that file holds a token-shaped value, because re-targeting a running vault feeds `root.key` to safe in the same way. A file that is already in that form is left untouched.

A new vault is held to the same standard. When ocfp starts one, it takes the new vault's unseal key only from that start's own log or pane, and never from the log of an earlier start, which may hold the key of a vault this one replaced. When it cannot save both the root token and the unseal key in a valid shape, the command exits non-zero. The error says that the vault is still running and names every vault log that exists, newest first, so we know where to look for the full keys. It never prints a key. The vault keeps running, and once it has started ocfp stops, archives, and deletes nothing, so we can save the keys from the log by hand as described above. When the new vault took the place of an archived one, the error also gives the archive's path.

## Errors and how to recover

| Error | What it means | What to do |
|-------|---------------|------------|
| `safe is too old for raft-backed inception vaults` | The safe on `PATH` cannot run a raft vault. | Install safe v1.25.0 or later and run the command again. Nothing on disk changed. |
| `the inception vault port is taken` | Something that is not this bloc's vault answers on the API port, such as another program or another bloc's vault on a colliding port. It can also be this bloc's own engine on file storage after its safe exited, because ocfp can no longer prove that engine is the bloc's. | Stop whatever holds the port, or set `OCFP_VAULT_INCEPTION_PORT` to move this bloc's ports. |
| `the inception vault cluster port is taken` | The cluster port, the API port plus 1000, is in use, so the engine could not bind it. | Free the cluster port, or set `OCFP_VAULT_INCEPTION_PORT` to move both ports. |
| `cannot tell whose vault holds the inception vault port` | `lsof`, `tmux`, or `ps` is missing or failed, so ocfp cannot tell whose vault answers on the port. | Make sure `lsof`, `tmux`, and `ps` are installed and on `PATH`, then run the command again. |
| `cannot tell whether the inception vault data is still in use` | `lsof` is missing or failed, so ocfp cannot tell whether an engine still holds `vault.db`. | Make sure `lsof` is installed and on `PATH`, then run the command again. |
| `the inception vault data holds both raft and file storage` | The data directory has both `core/` and raft data, which ocfp never creates on its own. | Inspect the directory by hand and decide which store to keep. ocfp will not touch it. |
| `inception vault did not stop` | A process still holds `vault.db` or the port after the stop. | Find the process with `lsof -t <data>/vault.db` and stop it, then run the command again. |
| `the inception vault is running but cannot be re-targeted` | A healthy vault runs, but safe has no target for it and `root.key` is empty. | Recover the root token, write it to `root.key`, and run the command again. |
| `the vault engine cannot migrate storage` | The engine has no `operator migrate` command. | Point `SAFE_ENGINE` and `PATH` at HashiCorp Vault or OpenBao 2.7 or earlier, or migrate by hand. |
| `the vault engine cannot read file storage` | The engine no longer supports file storage. The data was left file-backed. | Point `SAFE_ENGINE` and `PATH` at an engine that can read it, or migrate by hand. |
| `the file-to-raft migration failed` | `operator migrate` failed. The staging directory moved to `data.raft-failed-<timestamp>`, and `data` is unchanged. | Read `raft-migration-<timestamp>.log` in the log directory, fix the cause, and run the command again. |
| `the inception vault did not restart after its migration to raft` | The raft data is in place, but the vault did not open on it. Nothing was archived. | Read the vault log. The error names both the raft data and the file backup, so we can move the backup back to `data` to return to the old vault. |
| `the raft migration journal cannot be trusted` | The journal is unreadable or describes a migration ocfp could not have left. | Inspect the journal and the directories beside `data`, put them right by hand, and remove the journal. |
| `the interrupted raft migration cannot be finished safely` | A `swapping` journal exists, but the directories match none of the points at which a swap can stop. | Inspect the named paths by hand. ocfp moved nothing. |
| `an inception vault key file cannot be read` | `root.key` or `unseal.keys` exists but this user cannot read it, often because a `sudo` run left it owned by root. Nothing was started, stopped, or changed. | Give the file back to this user with mode 0600, for example with `sudo chown "$USER" <file>` and `chmod 600 <file>`, and run the command again. |
| `the inception vault unseal key file does not hold a whole key` | `unseal.keys` is cut short and no log or pane history still holds the whole key. | Follow the steps in the section on a cut-short unseal key, then run the command again. Nothing on disk changed. |
| `the inception vault's keys were not saved` | A vault is running, but its root token or unseal key is not saved in a valid shape. Either a new vault's keys could not be captured, or a running vault's keys were never saved and could not be recovered. | Find the full keys in the vault logs that the error names, write them to `root.key` and `unseal.keys` at mode 0600, and run the command again. The vault was left running. |
| `no free name left to keep an inception vault file` | ocfp tried to keep a copy of a root token beside `root.key`, or to keep an older vault log, but every name it tried was already taken. | Move old `root.key.saferc-*`, `root.key.rejected-*`, or `vault-inception.log.previous-*` files somewhere safe, and run the command again. |
| `timed out waiting for another ocfp run to release its lock` | Another ocfp run has worked on this bloc's vault for more than five minutes. | Wait for that run to finish, or stop it, and run the command again. A killed run releases the lock on its own. |

## Testing on a workstation

`scripts/smoke/inception-vault-raft.sh` exercises every path on this page against throwaway blocs on scratch ports, with a scratch `HOME`, scratch ocfp homes, and its own tmux server. It never touches a real bloc, the real `~/.saferc`, or a lab. CI does not run it, because it needs safe v1.25.0, an OpenBao engine, and an OpenBao 2.6.4 binary to create file-backed vaults with.

We run it from the ocfp repository with the safe repository checked out beside it.

```bash
SAFE_REPO=../safe scripts/smoke/inception-vault-raft.sh
```

The script prints PASS or FAIL for each case and keeps its scratch directory for inspection, printing its path at the end. It never prints a token, an unseal key, or a secret value, and it compares hashes instead. The header of the script lists the variables that point it at other binaries or ports.
