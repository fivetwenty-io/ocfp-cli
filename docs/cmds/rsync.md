# RSync Command

Synchronize files between the local machine and the bastion host using rsync.

## Overview

The `ocfp rsync` command synchronizes files bidirectionally between your local machine and a bastion host in the OCFP environment. It uses rsync over SSH, providing delta transfers (only changed portions of files are sent), which is significantly more efficient than SCP for large or frequently updated directories.

Remote paths use the `bastion:` prefix convention, which is automatically resolved to the bastion host's public IP address discovered from the bloc configuration.

The `--bloc` flag is required to identify which environment to connect to.

## Usage

```
ocfp rsync [flags] <source> <destination>
```

Exactly two arguments are required: a source and a destination. Prefix either with `bastion:` to indicate the remote side.

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
| `--exclude` | | | Exclude files matching pattern (repeatable) |
| `--include` | | | Include files matching pattern (repeatable) |
| `--rsync-options` | | | Additional rsync options passed through |

## SSH Key Resolution

When `--key` is not specified, the SSH key is searched in the following order:

1. `~/.ocfp/{bloc}/ssh/id_ed25519` (preferred)

2. `~/.ocfp/{bloc}/ssh/id_rsa` (fallback)

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

## See Also

- [scp](scp.md) for simple file copies
- [README](../../README.md) for full command reference
