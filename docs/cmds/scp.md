# SCP Command

Copy files between the local machine and the bastion host using SCP.

## Overview

The `ocfp scp` command transfers files bidirectionally between your local machine and a bastion host in the OCFP environment. Remote paths use the `bastion:` prefix convention, which is automatically resolved to the bastion host's public IP address discovered from the bloc configuration.

The `--bloc` flag is required to identify which environment to connect to.

## Usage

```
ocfp scp [flags] <source> <destination>
```

Exactly two arguments are required: a source and a destination. Prefix either with `bastion:` to indicate the remote side.

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--bloc` | | *(required)* | Bloc name for environment discovery |
| `--user` | | `ubuntu` | Username for SCP login |
| `--key` | | *(auto-detected)* | Path to SSH private key |
| `-r`, `--recursive` | `-r` | `false` | Recursively copy directories |
| `--scp-options` | | | Additional SCP options passed through |

## SSH Key Resolution

When `--key` is not specified, the SSH key is searched in the following order:

1. `~/.local/share/ocfp/{bloc}/ssh/id_ed25519` (preferred)

2. `~/.local/share/ocfp/{bloc}/ssh/id_rsa` (fallback)

These paths are the current layout. A bloc whose keys still sit in the legacy `~/.ocfp/{bloc}/ssh` directory keeps using them, even when an empty `ssh` directory also exists under `~/.local/share/ocfp`, and ocfp prints a one-time notice that the legacy path is in use. When both directories hold keys, ocfp uses the ones under `~/.local/share/ocfp` and warns if they differ, unless only the legacy directory holds an `id_ed25519`, in which case it uses the legacy directory and still warns.

Key permissions are verified and automatically corrected to `0600` if needed.

## Examples

Copy a file to bastion:

```bash
ocfp scp --bloc production /local/file.txt bastion:/tmp/
```

Copy a file from bastion:

```bash
ocfp scp --bloc production bastion:/etc/config.yml ./config.yml
```

Recursive directory copy:

```bash
ocfp scp --bloc production -r /local/dir/ bastion:/remote/dir/
```

Using a specific SSH key:

```bash
ocfp scp --bloc production --key ~/.ssh/custom-key.pem file.txt bastion:/tmp/
```

Passing extra SCP options (e.g., compression):

```bash
ocfp scp --bloc production --scp-options "-C" /local/largefile.tar.gz bastion:/tmp/
```

## See Also

- [rsync](rsync.md) for delta-based synchronization
- [README](../../README.md) for full command reference
