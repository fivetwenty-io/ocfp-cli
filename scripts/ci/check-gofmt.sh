#!/usr/bin/env bash
# Fails when gofmt -s would rewrite any file. Shared by ci.yml and `make preflight`.
set -euo pipefail

if [ "$(gofmt -s -l . | wc -l)" -gt 0 ]; then
  echo "Found unformatted files:"
  gofmt -s -l .
  exit 1
fi
