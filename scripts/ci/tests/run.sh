#!/usr/bin/env bash
# Runs every test_*.sh beside this file and fails if any of them fails.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
failed=0
for t in test_*.sh; do
  echo "== $t"
  bash "$t" || failed=1
done
exit "$failed"
