#!/usr/bin/env bash
set -euo pipefail

# 05-stratos-smoke.sh — Verify branded Stratos console at Wayne CF.
#
# Runs FROM the operator Mac (DNS resolves via cloudflared tunnel).
# Requires: dig, curl, cf, safe CLIs installed; vault unsealed and safe targeted.
#
# Console URL:  https://console.cf.wayne.pve.lab.fivetwenty.io
# CF API:       https://api.cf.wayne.pve.lab.fivetwenty.io
# UAA/login:    https://login.cf.wayne.pve.lab.fivetwenty.io
# CF app:       org=system  space=stratos  name=console

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

CONSOLE_FQDN="${CONSOLE_FQDN:-console.cf.wayne.pve.lab.fivetwenty.io}"
CF_API_FQDN="${CF_API_FQDN:-api.cf.wayne.pve.lab.fivetwenty.io}"
UAA_FQDN="${UAA_FQDN:-login.cf.wayne.pve.lab.fivetwenty.io}"
ADMIN_PASS_VAULT_PATH="${ADMIN_PASS_VAULT_PATH:-secret/exodus/ocfp-pve-wayne-cf/cf:admin_password}"
CF_ORG="${CF_ORG:-system}"
CF_SPACE="${CF_SPACE:-stratos}"
CF_APP="${CF_APP:-console}"
CURL_TIMEOUT="${CURL_TIMEOUT:-15}"

# ---------------------------------------------------------------------------
# Logging helpers
# ---------------------------------------------------------------------------

log_info()  { printf '[INFO]  %s\n' "$*"; }
log_ok()    { printf '[OK]    %s\n' "$*"; }
log_warn()  { printf '[WARN]  %s\n' "$*"; }
log_error() { printf '[ERROR] %s\n' "$*" >&2; }

die() {
  log_error "$*"
  exit 1
}

# ---------------------------------------------------------------------------
# Preflight: required tools
# ---------------------------------------------------------------------------

log_info "=== Stratos smoke test — $(date -u '+%Y-%m-%dT%H:%M:%SZ') ==="
log_info "Console: https://${CONSOLE_FQDN}"
log_info "CF API:  https://${CF_API_FQDN}"
log_info "CF app:  ${CF_ORG}/${CF_SPACE}/${CF_APP}"

for tool in dig curl safe cf; do
  command -v "${tool}" >/dev/null 2>&1 || die "required tool not found on PATH: ${tool}"
done
log_ok "Required tools on PATH: dig curl safe cf"

# ---------------------------------------------------------------------------
# Step 1: DNS resolves for console FQDN
# ---------------------------------------------------------------------------

log_info "Step 1: DNS resolution — dig +short ${CONSOLE_FQDN}"

DNS_OUT="$(dig +short "${CONSOLE_FQDN}" 2>/dev/null)"

if [[ -z "${DNS_OUT}" ]]; then
  die "Step 1 FAILED: dig returned no records for ${CONSOLE_FQDN} — cloudflared tunnel DNS not propagated or misconfigured"
fi

printf '%s\n' "${DNS_OUT}"

# Cloudflare proxy IPs are within 104.16.0.0/12 or 172.64.0.0/13 or similar;
# we accept any non-empty result (CNAME chain to Cloudflare is normal).
# Warn if the first address does not look like a public IP (e.g. RFC-1918).
FIRST_IP="$(printf '%s\n' "${DNS_OUT}" | grep -E '^[0-9]+\.' | head -1 || true)"
if [[ -n "${FIRST_IP}" ]]; then
  OCTET1="$(printf '%s' "${FIRST_IP}" | cut -d. -f1)"
  OCTET2="$(printf '%s' "${FIRST_IP}" | cut -d. -f2)"
  if [[ "${OCTET1}" -eq 10 ]] \
     || [[ "${OCTET1}" -eq 172 && "${OCTET2}" -ge 16 && "${OCTET2}" -le 31 ]] \
     || [[ "${OCTET1}" -eq 192 && "${OCTET2}" -eq 168 ]]; then
    log_warn "Step 1: resolved to RFC-1918 address ${FIRST_IP} — expected Cloudflare public IP (tunnel may route internally; proceeding)"
  else
    log_ok "Step 1: resolved to ${FIRST_IP} (public IP, expected Cloudflare proxy)"
  fi
else
  log_ok "Step 1: DNS resolved (CNAME chain — no A record at leaf)"
fi

# ---------------------------------------------------------------------------
# Step 2: HTTPS 200 and security headers
# ---------------------------------------------------------------------------

log_info "Step 2: HTTPS response — curl -sI https://${CONSOLE_FQDN}"

# Fetch headers separately for the security-header check; use -w '%{http_code}'
# for status to avoid HTTP/2 awk parsing pitfalls (no space between HTTP/2 and code).
HTTP_HEADERS="$(curl --max-time "${CURL_TIMEOUT}" -sI "https://${CONSOLE_FQDN}" 2>/dev/null)" \
  || die "Step 2 FAILED: curl returned non-zero for https://${CONSOLE_FQDN} — check DNS and cloudflared tunnel"

printf '%s\n' "${HTTP_HEADERS}"

HTTP_STATUS="$(curl --max-time "${CURL_TIMEOUT}" -sI -o /dev/null -w '%{http_code}' \
  "https://${CONSOLE_FQDN}" 2>/dev/null)" || true

if [[ "${HTTP_STATUS}" != "200" ]]; then
  die "Step 2 FAILED: expected HTTP 200, got '${HTTP_STATUS:-no-response}' — Stratos app may not be running or route not mapped"
fi
log_ok "Step 2: HTTP ${HTTP_STATUS} from https://${CONSOLE_FQDN}"

# Check for security headers (warn-only; Stratos may omit some)
for hdr in "x-frame-options" "x-content-type-options" "content-type"; do
  if printf '%s\n' "${HTTP_HEADERS}" | grep -qi "^${hdr}:"; then
    log_ok "Step 2: header present — ${hdr}"
  else
    log_warn "Step 2: header absent — ${hdr} (Stratos SPA may not set all security headers)"
  fi
done

# ---------------------------------------------------------------------------
# Step 3: HTML body contains "stratos" token
# ---------------------------------------------------------------------------

log_info "Step 3: HTML content — curl -s https://${CONSOLE_FQDN} | grep -qi 'stratos'"

HTML_BODY="$(curl --max-time "${CURL_TIMEOUT}" -s "https://${CONSOLE_FQDN}" 2>/dev/null)" \
  || die "Step 3 FAILED: curl returned non-zero fetching HTML body"

if [[ -z "${HTML_BODY}" ]]; then
  die "Step 3 FAILED: response body is empty — app returned no HTML"
fi

if ! printf '%s\n' "${HTML_BODY}" | grep -qi "stratos"; then
  die "Step 3 FAILED: response body does not contain 'stratos' — app may be misconfigured or wrong app is serving"
fi
log_ok "Step 3: 'stratos' token found in HTML body"

# ---------------------------------------------------------------------------
# Step 4: FiveTwenty brand asset present
#
# The branded Stratos build (dist/frontend/browser/) ships FiveTwenty assets at:
#   /core/assets/fivetwenty-logo.svg      (SVG — primary brand logo)
#   /core/assets/fivetwenty-logo-full.png (PNG — full wordmark)
# These paths are declared in assets/company-config.json under .logos.main.
# The default upstream logo lives at /core/assets/logo.png.
# ---------------------------------------------------------------------------

log_info "Step 4: Brand asset — https://${CONSOLE_FQDN}/core/assets/fivetwenty-logo.svg"

BRAND_STATUS="$(curl --max-time "${CURL_TIMEOUT}" -sI -o /dev/null -w '%{http_code}' \
  "https://${CONSOLE_FQDN}/core/assets/fivetwenty-logo.svg" 2>/dev/null)" \
  || true

if [[ "${BRAND_STATUS}" == "200" ]]; then
  log_ok "Step 4: FiveTwenty brand asset /core/assets/fivetwenty-logo.svg returns HTTP 200"
else
  # Try PNG fallback (full wordmark)
  PNG_STATUS="$(curl --max-time "${CURL_TIMEOUT}" -sI -o /dev/null -w '%{http_code}' \
    "https://${CONSOLE_FQDN}/core/assets/fivetwenty-logo-full.png" 2>/dev/null)" \
    || true
  if [[ "${PNG_STATUS}" == "200" ]]; then
    log_ok "Step 4: FiveTwenty brand asset /core/assets/fivetwenty-logo-full.png returns HTTP 200"
  else
    # WARN not fail — brand assets land when the FiveTwenty theme overlay is built and pushed.
    # Expected path once branding is deployed: /core/assets/fivetwenty-logo.svg
    log_warn "Step 4: FiveTwenty brand assets not found (svg=${BRAND_STATUS:-no-response} png=${PNG_STATUS:-no-response}) — re-run after 'bun run prebuild-ui && cf push'"
  fi
fi

# ---------------------------------------------------------------------------
# Step 5: CF API endpoint reachable from operator Mac
# ---------------------------------------------------------------------------

log_info "Step 5: CF API reachable — https://${CF_API_FQDN}/v2/info"

# Use -w '%{http_code}' to avoid HTTP/2 header-parsing pitfalls; fetch body
# separately only for JSON validation.
CF_API_STATUS="$(curl --max-time "${CURL_TIMEOUT}" -sk -o /dev/null -w '%{http_code}' \
  "https://${CF_API_FQDN}/v2/info" 2>/dev/null)" || true

if [[ "${CF_API_STATUS}" != "200" ]]; then
  die "Step 5 FAILED: expected HTTP 200 from CF API /v2/info, got '${CF_API_STATUS:-no-response}'"
fi

CF_API_BODY="$(curl --max-time "${CURL_TIMEOUT}" -sk "https://${CF_API_FQDN}/v2/info" 2>/dev/null)" || true
if ! printf '%s\n' "${CF_API_BODY}" | grep -qi '"api_version"'; then
  die "Step 5 FAILED: CF API /v2/info body does not look like CF info JSON — API may be down"
fi
log_ok "Step 5: CF API /v2/info returns HTTP ${CF_API_STATUS} with valid JSON"

# ---------------------------------------------------------------------------
# Step 6: UAA endpoint reachable
# ---------------------------------------------------------------------------

log_info "Step 6: UAA reachable — curl -sI https://${UAA_FQDN}/info"

UAA_BODY="$(curl --max-time "${CURL_TIMEOUT}" -sk "https://${UAA_FQDN}/info" 2>/dev/null)" \
  || die "Step 6 FAILED: curl returned non-zero for https://${UAA_FQDN}/info"

if [[ -z "${UAA_BODY}" ]]; then
  die "Step 6 FAILED: UAA /info response is empty"
fi

if ! printf '%s\n' "${UAA_BODY}" | grep -qi '"app":\|"commit_id"\|"entityID"\|"issuer"'; then
  log_warn "Step 6: UAA /info body does not contain expected JSON keys — UAA may be unhealthy or returning an error page"
else
  log_ok "Step 6: UAA /info endpoint reachable and returning info JSON"
fi

# ---------------------------------------------------------------------------
# Step 7: CF login and target app
# ---------------------------------------------------------------------------

log_info "Step 7: CF login — resolving admin password from vault"

ADMIN_PASS="$(safe get "${ADMIN_PASS_VAULT_PATH}" 2>/dev/null)" \
  || die "Step 7 FAILED: cannot read admin password from vault path ${ADMIN_PASS_VAULT_PATH} — is vault unsealed and safe targeted?"

[[ -n "${ADMIN_PASS}" ]] \
  || die "Step 7 FAILED: admin password is empty at ${ADMIN_PASS_VAULT_PATH}"

log_ok "Step 7: admin password resolved from vault"

log_info "Step 7: cf api https://${CF_API_FQDN} --skip-ssl-validation"

cf api "https://${CF_API_FQDN}" --skip-ssl-validation >/dev/null \
  || die "Step 7 FAILED: cf api returned non-zero for https://${CF_API_FQDN}"

log_info "Step 7: cf login -u admin"

CF_HOME_TMP="$(mktemp -d /tmp/stratos-smoke-cf-XXXXXX)"
trap 'rm -rf "${CF_HOME_TMP}"' EXIT

CF_HOME="${CF_HOME_TMP}" cf login \
  -a "https://${CF_API_FQDN}" \
  --skip-ssl-validation \
  -u admin \
  -p "${ADMIN_PASS}" \
  -o "${CF_ORG}" \
  -s "${CF_SPACE}" \
  >/dev/null \
  || die "Step 7 FAILED: cf login returned non-zero — check admin password or CF API availability"

log_ok "Step 7: logged in as admin, org=${CF_ORG} space=${CF_SPACE}"

log_info "Step 7: cf target -o ${CF_ORG} -s ${CF_SPACE}"

CF_HOME="${CF_HOME_TMP}" cf target \
  -o "${CF_ORG}" \
  -s "${CF_SPACE}" \
  >/dev/null \
  || die "Step 7 FAILED: cf target returned non-zero for org=${CF_ORG} space=${CF_SPACE}"

log_ok "Step 7: targeted ${CF_ORG}/${CF_SPACE}"

# ---------------------------------------------------------------------------
# Step 8: App is running 1/1
# ---------------------------------------------------------------------------

log_info "Step 8: App health — cf app ${CF_APP}"

APP_OUT="$(CF_HOME="${CF_HOME_TMP}" cf app "${CF_APP}" 2>/dev/null)" \
  || die "Step 8 FAILED: cf app ${CF_APP} returned non-zero — app may not exist in ${CF_ORG}/${CF_SPACE}"

printf '%s\n' "${APP_OUT}"

if ! printf '%s\n' "${APP_OUT}" | grep -qi "started"; then
  die "Step 8 FAILED: app '${CF_APP}' state is not 'started' — check 'cf app ${CF_APP}' and 'cf events ${CF_APP}'"
fi
log_ok "Step 8: app '${CF_APP}' is started"

# Check instance count — expect at least 1/N running (not 0/N)
INSTANCE_LINE="$(printf '%s\n' "${APP_OUT}" | grep -E '^instances:' | head -1 || true)"
if [[ -n "${INSTANCE_LINE}" ]]; then
  RUNNING_INSTANCES="$(printf '%s\n' "${INSTANCE_LINE}" | awk '{print $2}' | cut -d/ -f1)"
  if [[ "${RUNNING_INSTANCES}" -eq 0 ]] 2>/dev/null; then
    die "Step 8 FAILED: 0 instances running for '${CF_APP}' — app crashed or failed to start"
  fi
  log_ok "Step 8: instance count ${INSTANCE_LINE##*: }"
fi

# ---------------------------------------------------------------------------
# Step 9: Logs sanity — no FATAL/ERROR in recent output
# ---------------------------------------------------------------------------

log_info "Step 9: Recent logs — cf logs ${CF_APP} --recent | tail -20"

RECENT_LOGS="$(CF_HOME="${CF_HOME_TMP}" cf logs "${CF_APP}" --recent 2>/dev/null | tail -20)" \
  || die "Step 9 FAILED: cf logs ${CF_APP} --recent returned non-zero"

if [[ -z "${RECENT_LOGS}" ]]; then
  log_warn "Step 9: no recent log lines — app may have just started or log buffer is empty"
else
  printf '%s\n' "${RECENT_LOGS}"
  FATAL_LINES="$(printf '%s\n' "${RECENT_LOGS}" | grep -iE '\bFATAL\b|\bERROR\b' || true)"
  if [[ -n "${FATAL_LINES}" ]]; then
    log_warn "Step 9: FATAL/ERROR lines found in recent logs (shown above) — review before promoting to production:"
    printf '%s\n' "${FATAL_LINES}" | while IFS= read -r line; do
      log_warn "  ${line}"
    done
  else
    log_ok "Step 9: no FATAL/ERROR in recent log lines"
  fi
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

log_ok "=== Stratos smoke test PASSED — ${CF_APP} at https://${CONSOLE_FQDN} healthy ==="
exit 0
