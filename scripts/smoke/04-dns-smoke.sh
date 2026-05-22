#!/usr/bin/env bash
set -euo pipefail

# 04-dns-smoke.sh — Verify wayne DNS + Cloudflare tunnel chain post W5i+W5j cutover.
#
# Runs FROM the operator Mac (validates public-side DNS).
# Requires: dig, curl, openssl on PATH.
# Optional tools: cloudflared (Step 8), flarectl + CF_API_KEY (Step 9).
#
# Bloc:    wayne
# Infra:   HAProxy 10.64.64.50 behind Cloudflare tunnel ocfp-wayne
# Zone:    lab.fivetwenty.io
#
# Required checks (any failure → non-zero exit):
#   1  Apex CF API DNS       api.<DOMAIN_SUFFIX>
#   2  Wildcard apps DNS     <TEST_APP>.<DOMAIN_SUFFIX>
#   3  Login / UAA DNS       login.<DOMAIN_SUFFIX>
#   4  Console (Stratos) DNS console.<DOMAIN_SUFFIX>
#   5  HAProxy ping          10.64.64.50  (only when RUN_INTERNAL is set)
#   6  Tunnel HTTP           /v2/info 200, console 200
#   7  SSL chain             issuer must include Cloudflare
#
# Optional checks (skipped cleanly when tool/env-var absent):
#   8  cloudflared tunnel info ocfp-wayne
#   9  flarectl dns list for bloc records

# ---------------------------------------------------------------------------
# Configuration — all overridable via environment
# ---------------------------------------------------------------------------

BLOC="${BLOC:-wayne}"
DOMAIN_SUFFIX="${DOMAIN_SUFFIX:-ocf.wayne.lab.fivetwenty.io}"
TEST_APP="${TEST_APP:-test-app}"
HAPROXY_IP="${HAPROXY_IP:-10.64.64.50}"
TUNNEL_NAME="${TUNNEL_NAME:-ocfp-wayne}"
CF_ZONE="${CF_ZONE:-lab.fivetwenty.io}"

# Cloudflare publishes traffic through a shared anycast range.
# We accept any address that is NOT an RFC-1918 private address.
# The check rejects direct 10.x / 172.16-31.x / 192.168.x answers.
PRIVATE_PATTERN='^(10\.|172\.(1[6-9]|2[0-9]|3[01])\.|192\.168\.)'

# ---------------------------------------------------------------------------
# Tracking: required checks
# ---------------------------------------------------------------------------

FAILED_CHECKS=()

# ---------------------------------------------------------------------------
# Logging helpers
# ---------------------------------------------------------------------------

log_info()  { printf '[INFO]  %s\n' "$*"; }
log_pass()  { printf '[PASS]  %s\n' "$*"; }
log_warn()  { printf '[WARN]  %s\n' "$*"; }
log_error() { printf '[ERROR] %s\n' "$*" >&2; }
log_skip()  { printf '[SKIP]  %s\n' "$*"; }

mark_fail() {
  local label="$1"
  local detail="${2:-}"
  log_error "FAIL  ${label}${detail:+  (${detail})}"
  FAILED_CHECKS+=("${label}")
}

# ---------------------------------------------------------------------------
# Preflight: required tools
# ---------------------------------------------------------------------------

log_info "=== DNS smoke test — bloc=${BLOC}  domain_suffix=${DOMAIN_SUFFIX} ==="
log_info "Timestamp: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"

for tool in dig curl openssl; do
  command -v "${tool}" >/dev/null 2>&1 \
    || { log_error "required tool not found on PATH: ${tool}"; exit 1; }
done
log_pass "Required tools on PATH: dig curl openssl"

# ---------------------------------------------------------------------------
# Helper: resolve FQDN, return first answer line
# ---------------------------------------------------------------------------

resolve_fqdn() {
  dig +short "$1" 2>/dev/null | grep -v '^;' | head -1
}

# ---------------------------------------------------------------------------
# Helper: assert resolved IP is not an RFC-1918 private address
# ---------------------------------------------------------------------------

assert_public_ip() {
  local label="$1"
  local fqdn="$2"
  local resolved

  resolved="$(resolve_fqdn "${fqdn}")"

  if [[ -z "${resolved}" ]]; then
    mark_fail "${label}" "no DNS answer for ${fqdn}"
    return
  fi

  if printf '%s\n' "${resolved}" | grep -qE "${PRIVATE_PATTERN}"; then
    mark_fail "${label}" "resolved to private IP ${resolved} — expected Cloudflare proxy"
    return
  fi

  log_pass "${label}: ${fqdn} → ${resolved} (public/Cloudflare)"
}

# ---------------------------------------------------------------------------
# Step 1: Apex CF API
# ---------------------------------------------------------------------------

log_info "Step 1: Apex CF API — dig +short api.${DOMAIN_SUFFIX}"
assert_public_ip "Step 1 (apex api DNS)" "api.${DOMAIN_SUFFIX}"

# ---------------------------------------------------------------------------
# Step 2: Wildcard apps domain
# ---------------------------------------------------------------------------

log_info "Step 2: Wildcard apps — dig +short ${TEST_APP}.${DOMAIN_SUFFIX}"
assert_public_ip "Step 2 (wildcard apps DNS)" "${TEST_APP}.${DOMAIN_SUFFIX}"

# ---------------------------------------------------------------------------
# Step 3: Login / UAA
# ---------------------------------------------------------------------------

log_info "Step 3: Login UAA — dig +short login.${DOMAIN_SUFFIX}"
assert_public_ip "Step 3 (login UAA DNS)" "login.${DOMAIN_SUFFIX}"

# ---------------------------------------------------------------------------
# Step 4: Console (Stratos)
# ---------------------------------------------------------------------------

log_info "Step 4: Console (Stratos) — dig +short console.${DOMAIN_SUFFIX}"
assert_public_ip "Step 4 (console DNS)" "console.${DOMAIN_SUFFIX}"

# ---------------------------------------------------------------------------
# Step 5: HAProxy direct ping (internal only — RUN_INTERNAL gate)
# ---------------------------------------------------------------------------

if [[ -n "${RUN_INTERNAL:-}" ]]; then
  log_info "Step 5: HAProxy ping — ping -c1 -W2 ${HAPROXY_IP}"
  if ping -c1 -W2 "${HAPROXY_IP}" >/dev/null 2>&1; then
    log_pass "Step 5 (haproxy ping): ${HAPROXY_IP} reachable"
  else
    mark_fail "Step 5 (haproxy ping)" "${HAPROXY_IP} did not respond — run from bastion with RUN_INTERNAL=1"
  fi
else
  log_skip "Step 5 (haproxy ping): RUN_INTERNAL not set — skipping internal ping"
fi

# ---------------------------------------------------------------------------
# Step 6: Tunnel reachability via HTTPS
# ---------------------------------------------------------------------------

log_info "Step 6: Tunnel HTTP — https://api.${DOMAIN_SUFFIX}/v2/info"

http_code_api="$(curl -sI -o /dev/null -w '%{http_code}\n' \
  --max-time 15 \
  "https://api.${DOMAIN_SUFFIX}/v2/info" 2>/dev/null)"

if [[ "${http_code_api}" == "200" ]]; then
  log_pass "Step 6a (tunnel api /v2/info): HTTP ${http_code_api}"
else
  mark_fail "Step 6a (tunnel api /v2/info)" "expected 200, got ${http_code_api:-no response}"
fi

log_info "Step 6: Tunnel HTTP — https://console.${DOMAIN_SUFFIX}"

http_code_console="$(curl -sI -o /dev/null -w '%{http_code}\n' \
  --max-time 15 \
  --location \
  "https://console.${DOMAIN_SUFFIX}" 2>/dev/null)"

if [[ "${http_code_console}" =~ ^(200|301|302|303|307|308)$ ]]; then
  log_pass "Step 6b (tunnel console): HTTP ${http_code_console}"
else
  mark_fail "Step 6b (tunnel console)" "expected 2xx/3xx, got ${http_code_console:-no response}"
fi

# ---------------------------------------------------------------------------
# Step 7: SSL chain — issuer must include Cloudflare
# ---------------------------------------------------------------------------

log_info "Step 7: SSL chain — openssl s_client api.${DOMAIN_SUFFIX}:443"

ssl_out="$(echo \
  | openssl s_client \
      -connect "api.${DOMAIN_SUFFIX}:443" \
      -servername "api.${DOMAIN_SUFFIX}" \
      2>/dev/null \
  | openssl x509 -noout -issuer -subject 2>/dev/null)"

if [[ -z "${ssl_out}" ]]; then
  mark_fail "Step 7 (ssl chain)" "openssl returned no certificate — TLS handshake failed"
else
  printf '%s\n' "${ssl_out}"
  if printf '%s\n' "${ssl_out}" | grep -qi "cloudflare"; then
    log_pass "Step 7 (ssl chain): issuer contains Cloudflare"
  else
    mark_fail "Step 7 (ssl chain)" "issuer does not contain Cloudflare — check tunnel cert: ${ssl_out}"
  fi
fi

# ---------------------------------------------------------------------------
# Step 8: cloudflared tunnel info (optional — requires cloudflared on PATH)
# ---------------------------------------------------------------------------

if command -v cloudflared >/dev/null 2>&1; then
  log_info "Step 8: cloudflared tunnel info ${TUNNEL_NAME}"

  tunnel_out="$(cloudflared tunnel info "${TUNNEL_NAME}" 2>&1)" \
    || { log_warn "Step 8 (cloudflared): tunnel info failed — ${tunnel_out}"; tunnel_out=""; }

  if [[ -n "${tunnel_out}" ]]; then
    printf '%s\n' "${tunnel_out}"

    # A healthy tunnel shows at least one connection. The info output typically
    # includes a "Connections" table. We accept any line containing a connector
    # UUID or an explicit connection count > 0.
    if printf '%s\n' "${tunnel_out}" | grep -qiE "(connection|connector|active)"; then
      log_pass "Step 8 (cloudflared): tunnel ${TUNNEL_NAME} shows active connection(s)"
    else
      log_warn "Step 8 (cloudflared): tunnel ${TUNNEL_NAME} — no active connections visible; verify manually"
    fi
  fi
else
  log_skip "Step 8 (cloudflared): cloudflared not on PATH — skipping tunnel status"
fi

# ---------------------------------------------------------------------------
# Step 9: Cloudflare zone DNS records via flarectl (optional)
# ---------------------------------------------------------------------------

if command -v flarectl >/dev/null 2>&1 && [[ -n "${CF_API_KEY:-}" ]]; then
  log_info "Step 9: Cloudflare zone records — flarectl dns list --zone ${CF_ZONE}"

  zone_records="$(flarectl dns list --zone "${CF_ZONE}" 2>/dev/null)" \
    || { log_warn "Step 9 (flarectl): dns list failed"; zone_records=""; }

  if [[ -n "${zone_records}" ]]; then
    bloc_records="$(printf '%s\n' "${zone_records}" | grep "cf\.${BLOC}\.pve" || true)"

    if [[ -n "${bloc_records}" ]]; then
      printf '%s\n' "${bloc_records}"
      log_pass "Step 9 (flarectl): cf.${BLOC}.pve records found in ${CF_ZONE}"
    else
      log_warn "Step 9 (flarectl): no cf.${BLOC}.pve records found in zone ${CF_ZONE} — DNS cutover may be pending"
    fi
  fi
else
  log_skip "Step 9 (flarectl): flarectl not on PATH or CF_API_KEY not set — skipping zone record check"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

echo
log_info "=== DNS smoke test summary ==="

if [[ "${#FAILED_CHECKS[@]}" -eq 0 ]]; then
  log_pass "=== DNS smoke test PASSED — all required checks OK ==="
  exit 0
else
  log_error "=== DNS smoke test FAILED — ${#FAILED_CHECKS[@]} check(s) did not pass ==="
  for check in "${FAILED_CHECKS[@]}"; do
    log_error "  FAILED: ${check}"
  done
  exit 1
fi
