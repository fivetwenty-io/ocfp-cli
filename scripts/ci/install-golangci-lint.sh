#!/usr/bin/env bash
# Installs the golangci-lint release named in the version file into a bin
# directory, unless that directory already holds exactly that version.
# Usage: install-golangci-lint.sh <version-file> <bin-dir>
#
# CI runs the prebuilt release binary through golangci-lint-action, so this
# installs the same prebuilt binary, through golangci's own install script,
# which verifies the archive checksum. `go install` would compile it with
# the local Go toolchain instead, and the toolchain a golangci-lint is built
# with changes how it loads the module.
set -euo pipefail

if [ $# -ne 2 ] || [ ! -f "$1" ]; then
  echo "usage: $0 <version-file> <bin-dir>" >&2
  exit 2
fi
version="$(tr -d '[:space:]' <"$1")"
bin_dir="$2"
bin="${bin_dir}/golangci-lint"

# The output reads "golangci-lint has version 2.13.1 built with ...", so the
# trailing space keeps 2.13.1 from matching a pin of 2.13.
if [ -x "$bin" ] && "$bin" --version 2>/dev/null | grep -qF "has version ${version#v} "; then
  exit 0
fi

echo "Installing golangci-lint ${version} into ${bin_dir}..."
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh |
  sh -s -- -b "$bin_dir" "$version"
