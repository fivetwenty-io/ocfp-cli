#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
SCRIPT="$REPO_ROOT/scripts/ci/check-gofmt.sh"

new_sandbox
fake_cmd gofmt 'exit 0'
run_cmd bash "$SCRIPT"
assert_status 0 "clean tree passes"
assert_calls_contain "gofmt -s -l ." "uses gofmt -s -l ."

fake_cmd gofmt 'echo internal/foo/foo.go'
run_cmd bash "$SCRIPT"
assert_status 1 "unformatted file fails"
assert_out_contains "Found unformatted files:" "reports the heading"
assert_out_contains "internal/foo/foo.go" "names the file"

finish
