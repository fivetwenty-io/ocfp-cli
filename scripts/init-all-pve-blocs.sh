#!/usr/bin/env bash
set -euo pipefail

# Iterate over all PVE blocs defined in the user's merged config and run
# `ocfp init pve` against each. Use `yq` against the merged config so
# the script picks up whatever the user has defined — no hardcoded list.
#
# Vault populate is a separate step (operator runs `ocfp vault populate`
# per bloc after init).

CONFIG="${OCFP_CONFIG:-$HOME/.ocfp/config.pve.yml}"
if [ ! -f "$CONFIG" ]; then
  echo "config not found: $CONFIG" >&2
  exit 1
fi

BLOCS=$(yq -r '.blocs | keys | .[]' "$CONFIG" | grep '^ocfp-pve-' || true)
if [ -z "$BLOCS" ]; then
  echo "no ocfp-pve-* blocs found in $CONFIG" >&2
  exit 1
fi

for bloc in $BLOCS; do
  echo "=== $bloc ==="
  ocfp init pve --bloc "$bloc"
done
