#!/usr/bin/env bash
# Fails when a package whose tests reach ocfp's directories has no TestMain
# that calls testenv.Main or testenv.Run. Shared by ci.yml and
# `make preflight`.
#
# The check lives in a Go test that asks `go list` about every package. Go's
# test cache only tracks the files and environment variables the test
# reads, never what its subprocess reads, so a cached pass says nothing
# about a package added since. -count=1 makes the check run every time.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/../.."
exec go test -count=1 -run '^TestEveryPackageThatReachesOcfpPathsIsolatesItsTests$' ./internal/testenv/
