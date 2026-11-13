#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
SCRIPT="$REPO_ROOT/scripts/ci/check-test-isolation.sh"

new_sandbox
fake_cmd go 'exit 0'
run_cmd bash "$SCRIPT"
assert_status 0 "passes when the check passes"
assert_calls_contain "-count=1" "bypasses the test cache"
assert_calls_contain "-run ^TestEveryPackageThatReachesOcfpPathsIsolatesItsTests$" "runs only the isolation check"
assert_calls_contain "./internal/testenv/" "runs it in the testenv package"

fake_cmd go 'echo "FAIL: internal/zzleak"; exit 1'
run_cmd bash "$SCRIPT"
assert_status 1 "fails when the check fails"
assert_out_contains "internal/zzleak" "shows what the check reported"

finish
