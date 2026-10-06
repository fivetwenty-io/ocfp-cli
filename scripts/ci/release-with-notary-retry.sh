#!/usr/bin/env bash
# Runs goreleaser for a tag push and retries once when Apple's notary
# service times out.
#
# goreleaser mints an App Store Connect token that lives as long as the
# notarize timeout, and Apple caps that at 20 minutes, so a slow notary
# queue cannot be waited out by raising the timeout. Notarization runs
# before goreleaser publishes anything, so rerunning after a timeout is
# safe while no release exists for the tag. The script asks the GitHub API
# (with curl, since the golang job container has no gh CLI) and retries only
# on a 404, which relies on `draft: false` in .goreleaser.yaml: the releases
# API also answers 404 for a draft, so a draft release would look absent.
# A 200, any other status, a curl failure, or a missing
# GITHUB_TOKEN or GITHUB_REPOSITORY all fail without a rerun.
#
# A failure counts as a notary timeout only when a single output line holds
# "release failed", appstoreconnect.apple.com/notary, and "context deadline
# exceeded" together. Any other failure, and a second timeout, fails
# immediately.
#
# The tag comes from TAG, or GITHUB_REF_NAME when TAG is unset. This script
# never prints environment variables or request headers; the signing, tap,
# and API tokens reach their consumers through the environment only.
set -euo pipefail

tag="${TAG:-${GITHUB_REF_NAME:-}}"
if [ -z "$tag" ]; then
  echo "release: no tag; set TAG or GITHUB_REF_NAME" >&2
  exit 2
fi

log="$(mktemp)"
trap 'rm -f "$log"' EXIT

# --parallelism 1 builds the six targets one after another; see the GOFLAGS
# note at the top of release.yml.
#
# release_once runs goreleaser, streams its output, and keeps a copy in $log.
# pipefail makes the pipeline report goreleaser's status, not tee's.
release_once() {
  goreleaser release --clean --parallelism 1 2>&1 | tee "$log"
}

# A notary timeout is one log line that holds all three of the failure
# marker, the notary URL, and the deadline error. Matching the strings
# anywhere in the log would also catch a later, unrelated timeout (an asset
# upload, say) in a run that merely mentioned the notary URL earlier. ANSI
# color codes are stripped first, since they can split the phrases.
is_notary_timeout() {
  local esc
  esc="$(printf '\033')"
  sed "s/${esc}\[[0-9;]*[A-Za-z]//g" "$log" |
    grep -F 'release failed' |
    grep -F 'appstoreconnect.apple.com/notary' |
    grep -qF 'context deadline exceeded'
}

status=0
release_once || status=$?
if [ "$status" -eq 0 ]; then
  exit 0
fi

if ! is_notary_timeout; then
  exit "$status"
fi

echo "release: notary timeout on ${tag}; checking that no release exists before retrying" >&2

if [ -z "${GITHUB_TOKEN:-}" ] || [ -z "${GITHUB_REPOSITORY:-}" ]; then
  echo "release: GITHUB_TOKEN or GITHUB_REPOSITORY is unset, so we cannot confirm that no release exists; not retrying" >&2
  exit "$status"
fi

http_status=""
curl_rc=0
http_status="$(curl -s -o /dev/null -w '%{http_code}' --max-time 30 \
  -H "Authorization: Bearer ${GITHUB_TOKEN}" \
  -H "Accept: application/vnd.github+json" \
  "https://api.github.com/repos/${GITHUB_REPOSITORY}/releases/tags/${tag}")" || curl_rc=$?

if [ "$curl_rc" -ne 0 ]; then
  echo "release: could not query the release for ${tag} (curl exit ${curl_rc}); not retrying" >&2
  exit "$status"
fi
if [ "$http_status" = "200" ]; then
  echo "release: a release for ${tag} already exists; not retrying" >&2
  exit "$status"
fi
if [ "$http_status" != "404" ]; then
  echo "release: the releases API answered ${http_status} for ${tag}, not 404; not retrying" >&2
  exit "$status"
fi

echo "release: no release for ${tag}; retrying goreleaser once" >&2
release_once
