#!/usr/bin/env bash
# Cross-compiles every release target into dist/, stamping the same link
# flags the Makefile uses. Shared by ci.yml and `make preflight`.
#
# The lab runners are all linux/amd64, so one job builds the targets in
# sequence rather than a matrix; five simultaneous links of this dependency
# tree exhausted runner memory.
set -euo pipefail

pkg=github.com/ocfp/ocfp-cli-go/internal/version
version="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
ldflags="-s -w"
ldflags="${ldflags} -X ${pkg}.Version=${version}"
ldflags="${ldflags} -X ${pkg}.GitCommit=$(git rev-parse --short HEAD)"
ldflags="${ldflags} -X ${pkg}.BuildTime=$(date -u '+%Y-%m-%d_%H:%M:%S')"
ldflags="${ldflags} -X ${pkg}.GoVersion=$(go version | cut -d' ' -f3)"
mkdir -p dist
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
  goos="${target%/*}"
  goarch="${target#*/}"
  out="dist/ocfp-${goos}-${goarch}"
  if [ "$goos" = "windows" ]; then
    out="${out}.exe"
  fi
  echo "::group::build $target"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -ldflags "$ldflags" -o "$out" ./cmd/ocfp
  echo "::endgroup::"
done
ls -l dist
