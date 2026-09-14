# RSync Command

Synchronize files between the local machine and the bastion host using rsync.

## Overview

The `ocfp rsync` command synchronizes files bidirectionally between your local machine and a bastion host in the OCFP environment. It uses rsync over SSH, providing delta transfers (only changed portions of files are sent), which is significantly more efficient than SCP for large or frequently updated directories.

Remote paths use the `bastion:` prefix convention, which is automatically resolved to the bastion host's public IP address discovered from the bloc configuration.

The `--bloc` flag is required to identify which environment to connect to.

## Usage

```
ocfp rsync [flags] <source> <destination> [-- <rsync flag>...]
```

We require exactly two positional arguments, a source and a destination, and we prefix either one with `bastion:` to say which side is remote.

## Passing Any RSync Flag Through

We model only the rsync flags we reach for most often, and everything else goes to rsync through the `--` separator. Whatever we write after `--` is handed to rsync unchanged and in the order we wrote it, so any rsync flag at all works, including ones we have never named here.

```bash
ocfp rsync --bloc production /local/dir/ bastion:/remote/dir/ -- --info=progress2 --copy-links --filter=':- .gitignore'
```

The source and the destination stay in front of the separator, because ocfp needs them to resolve the `bastion:` prefix. Our own flags, such as `--bloc`, `--user`, and `--key`, are never forwarded to rsync, and nothing after the separator is interpreted by ocfp, so a flag that takes its own value keeps that value.

If we forget the separator, ocfp reports the unknown flag and reminds us how to pass it, so the fix is to move the flag after `--` and run the command again.

Ordering is worth knowing. We render the modelled flags first, then the `--exclude` and `--include` patterns in the order they were given, then anything from `--rsync-options`, and finally everything after the separator. When the relative order of a filter rule matters, we put the whole set of rules after the separator so that rsync sees them in exactly the order we wrote.

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--bloc` | | *(required)* | Bloc name for environment discovery |
| `--user` | | `ubuntu` | Username for rsync |
| `--key` | | *(auto-detected)* | Path to SSH private key |
| `-a`, `--archive` | `-a` | `true` | Archive mode (preserves permissions, ownership, timestamps) |
| `-z`, `--compress` | `-z` | `false` | Compress data during transfer |
| `-v`, `--verbose` | `-v` | `false` | Verbose output |
| `--delete` | | `false` | Delete files in destination not present in source |
| `--dry-run` | | `false` | Perform a trial run with no changes made |
| `-i`, `--itemize-changes` | `-i` | `false` | Output a change summary for every update |
| `--exclude` | | | Exclude files matching pattern (repeatable) |
| `--include` | | | Include files matching pattern (repeatable) |
| `--rsync-options` | | | Additional rsync options passed through |
| `--` | | | Everything after this separator goes to rsync unchanged |

## SSH Key Resolution

When `--key` is not specified, the SSH key is searched in the following order:

1. `~/.local/share/ocfp/{bloc}/ssh/id_ed25519` (preferred)

2. `~/.local/share/ocfp/{bloc}/ssh/id_rsa` (fallback)

Key permissions are verified and automatically corrected to `0600` if needed.

## When to Use RSync vs SCP

| Scenario | Recommended |
|----------|-------------|
| Single file transfer | `scp` |
| Large directory synchronization | `rsync` |
| Repeated transfers of changing files | `rsync` (delta transfers) |
| Mirror a directory exactly | `rsync --delete` |
| Quick one-off copy | `scp` |
| Need to resume interrupted transfer | `rsync` |
| Selective sync with patterns | `rsync` with `--exclude`/`--include` |

## Examples

Sync a directory to bastion:

```bash
ocfp rsync --bloc production /local/dir/ bastion:/remote/dir/
```

Sync from bastion to local:

```bash
ocfp rsync --bloc production bastion:/remote/dir/ /local/dir/
```

Dry run to preview what would be synced:

```bash
ocfp rsync --bloc production --dry-run /local/dir/ bastion:/remote/dir/
```

Mirror sync with `--delete` (removes extra files in destination):

```bash
ocfp rsync --bloc production --delete /local/dir/ bastion:/remote/dir/
```

Using exclude patterns:

```bash
ocfp rsync --bloc production --exclude "*.tmp" --exclude ".git" /local/dir/ bastion:/remote/dir/
```

Using include patterns with a broad exclude:

```bash
ocfp rsync --bloc production --include "*.yml" --exclude "*" /local/configs/ bastion:/remote/configs/
```

Combining common flags (`-avz`):

```bash
ocfp rsync --bloc production -a -v -z /local/dir/ bastion:/remote/dir/
```

Itemizing every change a dry run would make:

```bash
ocfp rsync --bloc production --dry-run --itemize-changes /local/dir/ bastion:/remote/dir/
```

Handing rsync a flag we do not model, in this case a progress meter and a filter file:

```bash
ocfp rsync --bloc production /local/dir/ bastion:/remote/dir/ -- --info=progress2 --filter=':- .gitignore'
```

## See Also

- [scp](scp.md) for simple file copies
- [README](../../README.md) for full command reference
