#!/usr/bin/env bash
# Smoke-tests a built ocfp binary: help, version, and the environment
# commands. Usage: integration-smoke.sh <path-to-binary>
#
# CI passes dist/ocfp-linux-amd64. `make preflight` passes the binary built
# for the host platform, since the Linux binary cannot run on macOS.
set -euo pipefail

if [ $# -ne 1 ]; then
  echo "usage: $0 <path-to-ocfp-binary>" >&2
  exit 2
fi
bin="$1"

chmod +x "$bin"

# Run with an empty home and config directory, as in CI's clean container, so
# a local run never reads the developer's real ocfp configuration.
home="$(mktemp -d)"
trap 'rm -rf "$home"' EXIT
export HOME="$home"
export XDG_CONFIG_HOME="$home/.config"

"$bin" --help
"$bin" version
# No bloc is configured on the runner, so these exercise the command wiring
# and argument parsing only; a missing environment is expected.
"$bin" env list || true
"$bin" env show || true
