#!/usr/bin/env bash
set -euo pipefail
# shellcheck source=lib.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
SCRIPT="$REPO_ROOT/scripts/ci/release-with-notary-retry.sh"

# What goreleaser prints when notarization times out: one line that holds
# the failure marker, the notary URL, and the deadline error together.
NOTARY_TIMEOUT='  * release failed after 20m0s error=failed to notarize: Post "https://appstoreconnect.apple.com/notary/v2/submissions": context deadline exceeded'

new_sandbox
# The fake goreleaser reads one outcome per call from $SANDBOX/outcomes (one
# per line) and exits with a distinct status per failure kind, so the tests
# can tell which call's status the script propagates.
#   ok            exit 0
#   timeout       the notary timeout line, exit 3
#   other         an unrelated failure, exit 4
#   half-notary   the notary URL without a deadline error, exit 5
#   half-deadline a deadline error without the notary URL, exit 6
#   split-lines   notary URL and deadline error on different lines, exit 7
#   no-marker     notary URL and deadline error without "release failed", exit 8
#   ansi          the timeout line wrapped in ANSI color codes, exit 3
fake_cmd goreleaser 'n=$(wc -l <"$SANDBOX/count" 2>/dev/null || echo 0); echo x >>"$SANDBOX/count"
outcome=$(sed -n "$((n + 1))p" "$SANDBOX/outcomes")
case "$outcome" in
  ok) echo "release succeeded"; exit 0;;
  timeout) echo "$NOTARY_TIMEOUT_LINE"; exit 3;;
  other) echo "build failed: compile error"; exit 4;;
  half-notary) echo "release failed: contacting appstoreconnect.apple.com/notary failed: 503"; exit 5;;
  half-deadline) echo "release failed: upload: context deadline exceeded"; exit 6;;
  split-lines) echo "notarizing via https://appstoreconnect.apple.com/notary/v2/submissions"
    echo "release failed after 3m0s error=upload: context deadline exceeded"; exit 7;;
  no-marker) echo "Post \"https://appstoreconnect.apple.com/notary/v2\": context deadline exceeded"; exit 8;;
  ansi) printf "\033[31m* release failed\033[0m after 20m0s error=Post \"https://appstoreconnect.apple.com/notary/v2\": context \033[2mdeadline exceeded\033[0m\n"; exit 3;;
esac'
# The fake curl prints $FAKE_CURL_STATUS (default 404, no release) the way
# -w '%{http_code}' would, and exits with $FAKE_CURL_EXIT. A real curl run
# with -v would write the request headers, token included, to stderr, which
# lands in the job log, so the fake does the same when it is asked to.
fake_cmd curl 'for a in "$@"; do
  case "$a" in
    -v | --verbose | -i | --include | --trace* | --dump-header) for h in "$@"; do echo "> $h" >&2; done;;
  esac
done
printf "%s" "${FAKE_CURL_STATUS:-404}"; exit "${FAKE_CURL_EXIT:-0}"'

# scenario sets the goreleaser outcomes and resets the counters.
scenario() {
  : >"$CALLS"
  : >"$SANDBOX/count"
  printf '%s\n' "$@" >"$SANDBOX/outcomes"
}

# Recognisable fake secret values. None of them may ever reach the output.
FAKE_API_TOKEN=ghs_FAKE0API0TOKEN0VALUE0123456789
FAKE_NOTARY_KEY=FAKE0NOTARY0KEY0MATERIAL0abcdef
FAKE_TAP_TOKEN=ghp_FAKE0TAP0TOKEN0VALUE0987654321
FAKE_P12_PASSWORD=FAKE0P12PASSWORD0hunter2
export NOTARY_TIMEOUT_LINE="$NOTARY_TIMEOUT"
export GITHUB_REF_NAME=v1.2.3
export GITHUB_REPOSITORY=fivetwenty-io/ocfp-cli
export GITHUB_TOKEN="$FAKE_API_TOKEN"
export MACOS_NOTARY_KEY="$FAKE_NOTARY_KEY"
export HOMEBREW_TAP_GITHUB_TOKEN="$FAKE_TAP_TOKEN"
export MACOS_SIGN_PASSWORD="$FAKE_P12_PASSWORD"

# release runs the script and checks that the output holds no secret value
# and no request header, whatever path the run took.
release() {
  local label="$1"
  shift
  run_cmd "$@"
  local secret
  for secret in "$FAKE_API_TOKEN" "$FAKE_NOTARY_KEY" "$FAKE_TAP_TOKEN" "$FAKE_P12_PASSWORD"; do
    assert_out_lacks "$secret" "no secret value in the output ($label)"
  done
  assert_out_lacks "Authorization" "no request header in the output ($label)"
}

scenario ok
release "first-try success" bash "$SCRIPT"
assert_status 0 "first-try success passes"
assert_call_count "goreleaser release --clean --parallelism 1" 1 "runs goreleaser once"
assert_calls_lack "curl" "does not consult the API on success"
assert_out_contains "release succeeded" "goreleaser output reaches the job log"

scenario timeout ok
release "timeout then success" bash "$SCRIPT"
assert_status 0 "notary timeout then success passes"
assert_call_count "goreleaser release --clean --parallelism 1" 2 "retries goreleaser once"
assert_calls_contain "https://api.github.com/repos/fivetwenty-io/ocfp-cli/releases/tags/v1.2.3" "checks for an existing release by tag"
assert_calls_contain "Authorization: Bearer $FAKE_API_TOKEN" "authenticates with the token"
assert_calls_contain "%{http_code}" "captures only the status code"
assert_out_contains "retrying" "announces the retry"

scenario ansi ok
release "ansi-colored timeout" bash "$SCRIPT"
assert_status 0 "an ANSI-colored timeout line is recognised"
assert_call_count "goreleaser release" 2 "retries after an ANSI-colored timeout"

scenario timeout timeout ok
release "two timeouts" bash "$SCRIPT"
assert_status 3 "two notary timeouts fail with goreleaser's status"
assert_call_count "goreleaser release --clean --parallelism 1" 2 "never runs a third time"

scenario timeout other
release "timeout then other failure" bash "$SCRIPT"
assert_status 4 "the retry's own failure status is the one propagated"

scenario other ok
release "non-notary failure" bash "$SCRIPT"
assert_status 4 "a non-notary failure fails right away with its own status"
assert_call_count "goreleaser release --clean --parallelism 1" 1 "does not retry a non-notary failure"
assert_calls_lack "curl" "does not consult the API for a non-notary failure"

scenario half-notary ok
release "notary url only" bash "$SCRIPT"
assert_status 5 "notary URL without a deadline error fails"
assert_call_count "goreleaser release" 1 "no retry on a notary URL alone"

scenario half-deadline ok
release "deadline only" bash "$SCRIPT"
assert_status 6 "deadline error without the notary URL fails"
assert_call_count "goreleaser release" 1 "no retry on a deadline error alone"

scenario split-lines ok
release "split lines" bash "$SCRIPT"
assert_status 7 "notary URL and deadline error on different lines fail"
assert_call_count "goreleaser release" 1 "no retry when the two strings are on different lines"
assert_calls_lack "curl" "does not consult the API for split lines"

scenario no-marker ok
release "no failure marker" bash "$SCRIPT"
assert_status 8 "a line without the release failed marker fails"
assert_call_count "goreleaser release" 1 "no retry without the release failed marker"

# Only a 404 proves no release exists; everything else fails without a rerun.
scenario timeout ok
FAKE_CURL_STATUS=200 release "existing release" bash "$SCRIPT"
assert_status 3 "a timeout with an existing release fails"
assert_call_count "goreleaser release" 1 "does not rerun over an existing release"
assert_out_contains "already exists" "explains why it will not retry"

scenario timeout ok
FAKE_CURL_STATUS=500 release "api 500" bash "$SCRIPT"
assert_status 3 "a 500 from the API fails"
assert_call_count "goreleaser release" 1 "does not retry on a 500"

scenario timeout ok
FAKE_CURL_STATUS=000 FAKE_CURL_EXIT=7 release "curl failure" bash "$SCRIPT"
assert_status 3 "a curl failure fails"
assert_call_count "goreleaser release" 1 "does not retry when curl fails"

scenario timeout ok
GITHUB_TOKEN='' release "missing token" bash "$SCRIPT"
assert_status 3 "a missing token fails"
assert_call_count "goreleaser release" 1 "does not retry without a token"
assert_calls_lack "curl" "does not call the API without a token"

scenario timeout ok
GITHUB_REPOSITORY='' release "missing repository" bash "$SCRIPT"
assert_status 3 "a missing repository fails"
assert_call_count "goreleaser release" 1 "does not retry without a repository"

unset GITHUB_REF_NAME
scenario timeout ok
release "missing tag" bash "$SCRIPT"
assert_status 2 "a missing tag is a usage error"
assert_call_count "goreleaser release" 0 "does not run goreleaser without a tag"

finish
