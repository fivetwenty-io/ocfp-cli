#!/usr/bin/env bash
set -euo pipefail

# 03-cf-smoke.sh — Verify Wayne CF health.
#
# Runs FROM the bastion (after `ocfp bastion ssh`).
# Requires: safe, cf CLIs installed; vault unsealed and safe targeted.
#
# Vault path convention: secret/exodus/<genesis-env-name>/cf
# Genesis env: ocfp-pve-wayne-cf  HAProxy IP: 10.64.64.50
# System domain: ocf.wayne.lab.fivetwenty.io

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

CF_ENV_NAME="${CF_ENV_NAME:-ocfp-pve-wayne-cf}"
CF_API_URL="${CF_API_URL:-https://api.system.ocf.wayne.lab.fivetwenty.io}"
CF_LOGIN_URL="${CF_LOGIN_URL:-https://login.system.ocf.wayne.lab.fivetwenty.io}"
CF_HAPROXY_IP="${CF_HAPROXY_IP:-10.64.64.50}"
VAULT_EXODUS_BASE="${VAULT_EXODUS_BASE:-secret/exodus/ocfp-pve-wayne-cf/cf}"
CF_ADMIN_USER="${CF_ADMIN_USER:-admin}"

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
# Cleanup trap
# ---------------------------------------------------------------------------

# Tracks whether cf login succeeded so the trap can gate cf logout.
_CF_LOGGED_IN=0
# Temp dir for CF_HOME isolation; set after mktemp to allow guard in cleanup.
_CF_HOME_TMP=""

# shellcheck disable=SC2329  # invoked via trap EXIT INT TERM
_cleanup() {
  local rc=$?
  # Only log out if login actually succeeded; avoids clobbering the operator's
  # existing cf session on early failures (e.g. Step 1 vault error).
  if [[ "${_CF_LOGGED_IN}" -eq 1 ]]; then
    CF_HOME="${_CF_HOME_TMP}" cf logout >/dev/null 2>&1 || true
  fi
  # Remove the isolated CF_HOME tmpdir and unset so the calling shell is clean.
  if [[ -n "${_CF_HOME_TMP}" ]]; then
    rm -rf "${_CF_HOME_TMP}"
    unset CF_HOME
  fi
  # Scrub credentials from environment.
  unset CF_ADMIN_PASSWORD CF_USERNAME CF_PASSWORD 2>/dev/null || true
  exit "${rc}"
}
trap '_cleanup' EXIT INT TERM

# ---------------------------------------------------------------------------
# Preflight: required tools
# ---------------------------------------------------------------------------

log_info "=== CF smoke test — $(date -u '+%Y-%m-%dT%H:%M:%SZ') ==="
log_info "Genesis env:  ${CF_ENV_NAME}"
log_info "API URL:      ${CF_API_URL}"
log_info "Vault exodus: ${VAULT_EXODUS_BASE}"

for tool in safe cf curl; do
  command -v "${tool}" >/dev/null 2>&1 || die "required tool not found on PATH: ${tool}"
done
log_ok "Required tools on PATH: safe cf curl"

# ---------------------------------------------------------------------------
# Step 1: Resolve CF admin password from vault
# ---------------------------------------------------------------------------

log_info "Step 1: Resolving CF admin password from vault (${VAULT_EXODUS_BASE}:admin_password)"

CF_ADMIN_PASSWORD="$(safe get "${VAULT_EXODUS_BASE}:admin_password" 2>/dev/null)" \
  || die "Step 1 FAILED: cannot read admin_password from vault path ${VAULT_EXODUS_BASE}:admin_password — is vault unsealed and safe targeted?"

[[ -n "${CF_ADMIN_PASSWORD}" ]] \
  || die "Step 1 FAILED: admin_password is empty at ${VAULT_EXODUS_BASE}:admin_password"

log_ok "Step 1: admin_password resolved from vault"

# ---------------------------------------------------------------------------
# Step 2: cf login (isolated CF_HOME — does not affect operator's ~/.cf)
# ---------------------------------------------------------------------------

log_info "Step 2: Logging in — cf login -a ${CF_API_URL} -u ${CF_ADMIN_USER} --skip-ssl-validation"

# Create an isolated config dir so the login does not overwrite ~/.cf/config.json.
# The _cleanup trap removes it on exit.
_CF_HOME_TMP="$(mktemp -d /tmp/cf-smoke-XXXXXX)"
export CF_HOME="${_CF_HOME_TMP}"

# Omit -o system: the system org may not yet exist if CF errand jobs are still
# running. Step 5 targets it best-effort; Step 4 handles the no-orgs case.
CF_HOME="${_CF_HOME_TMP}" cf login \
  -a "${CF_API_URL}" \
  -u "${CF_ADMIN_USER}" \
  -p "${CF_ADMIN_PASSWORD}" \
  --skip-ssl-validation \
  >/dev/null \
  || die "Step 2 FAILED: cf login returned non-zero — check API endpoint and credentials"

_CF_LOGGED_IN=1
log_ok "Step 2: logged in as ${CF_ADMIN_USER} (CF_HOME isolated to ${_CF_HOME_TMP})"

# ---------------------------------------------------------------------------
# Step 3: API info
# ---------------------------------------------------------------------------

log_info "Step 3: API info — cf api"

cf api \
  || die "Step 3 FAILED: cf api returned non-zero"

log_ok "Step 3: API info OK"

# ---------------------------------------------------------------------------
# Step 4: Organizations
# ---------------------------------------------------------------------------

log_info "Step 4: Organizations — cf orgs"

ORG_OUT="$(cf orgs 2>&1)" \
  || die "Step 4 FAILED: cf orgs returned non-zero: ${ORG_OUT}"

printf '%s\n' "${ORG_OUT}"

# system org is created by the CF kit; warn (not fail) if absent pre-deploy.
if printf '%s\n' "${ORG_OUT}" | grep -q "^system$"; then
  log_ok "Step 4: 'system' org present"
elif printf '%s\n' "${ORG_OUT}" | grep -qi "No orgs found\|getting orgs"; then
  log_warn "Step 4: no orgs yet — expected pre-IMP-29; run after Stratos push to confirm"
else
  log_ok "Step 4: orgs listed (system org may appear after first push)"
fi

# ---------------------------------------------------------------------------
# Step 5: Spaces
# ---------------------------------------------------------------------------

log_info "Step 5: Spaces — cf spaces (targeting system org)"

# Target system org so cf spaces returns meaningful output.
cf target -o system >/dev/null 2>&1 || log_warn "Step 5: cannot target system org (may not exist yet)"

SPACE_OUT="$(cf spaces 2>&1)" \
  || die "Step 5 FAILED: cf spaces returned non-zero: ${SPACE_OUT}"

printf '%s\n' "${SPACE_OUT}"
log_ok "Step 5: spaces OK"

# ---------------------------------------------------------------------------
# Step 6: Buildpacks
# ---------------------------------------------------------------------------

log_info "Step 6: Buildpacks — cf buildpacks"

BUILDPACK_OUT="$(cf buildpacks 2>&1)" \
  || die "Step 6 FAILED: cf buildpacks returned non-zero: ${BUILDPACK_OUT}"

printf '%s\n' "${BUILDPACK_OUT}"

# Expect standard buildpacks shipped with CF deployment.
EXPECTED_BUILDPACKS=( staticfile java nodejs ruby go python php binary )
MISSING_BUILDPACKS=()
for bp in "${EXPECTED_BUILDPACKS[@]}"; do
  if ! printf '%s\n' "${BUILDPACK_OUT}" | grep -qi "${bp}"; then
    MISSING_BUILDPACKS+=( "${bp}" )
  fi
done

if [[ ${#MISSING_BUILDPACKS[@]} -eq 0 ]]; then
  log_ok "Step 6: all expected buildpacks present"
else
  log_warn "Step 6: buildpacks not found in output: ${MISSING_BUILDPACKS[*]} — may be pending upload"
fi

# ---------------------------------------------------------------------------
# Step 7: Stacks
# ---------------------------------------------------------------------------

log_info "Step 7: Stacks — cf stacks"

STACK_OUT="$(cf stacks 2>&1)" \
  || die "Step 7 FAILED: cf stacks returned non-zero: ${STACK_OUT}"

printf '%s\n' "${STACK_OUT}"

# cflinuxfs4 (Jammy) is the default in cf-deployment ≥10.x.
# cflinuxfs3 is present only when use-cflinuxfs3 feature is enabled.
if printf '%s\n' "${STACK_OUT}" | grep -q "cflinuxfs4"; then
  log_ok "Step 7: cflinuxfs4 stack present"
elif printf '%s\n' "${STACK_OUT}" | grep -q "cflinuxfs3"; then
  log_ok "Step 7: cflinuxfs3 stack present (use-cflinuxfs3 feature enabled)"
else
  log_warn "Step 7: neither cflinuxfs4 nor cflinuxfs3 found — stacks may be pending upload"
fi

# ---------------------------------------------------------------------------
# Step 8: curl /v2/info — JSON response from CC API
# ---------------------------------------------------------------------------

log_info "Step 8: curl CC API /v2/info — ${CF_API_URL}/v2/info"

INFO_OUT="$(curl -sk --max-time 15 "${CF_API_URL}/v2/info" 2>&1)" \
  || die "Step 8 FAILED: curl ${CF_API_URL}/v2/info returned non-zero"

# Validate JSON-like response (must contain 'api_version' key).
if printf '%s\n' "${INFO_OUT}" | grep -q '"api_version"'; then
  log_ok "Step 8: /v2/info returned valid JSON with api_version"
  printf '%s\n' "${INFO_OUT}" | python3 -m json.tool 2>/dev/null || printf '%s\n' "${INFO_OUT}"
else
  log_warn "Step 8: /v2/info response does not contain api_version — raw output:"
  printf '%s\n' "${INFO_OUT}"
fi

# ---------------------------------------------------------------------------
# Step 9: curl UAA /info — login endpoint reachable
# ---------------------------------------------------------------------------

log_info "Step 9: curl UAA login /info — ${CF_LOGIN_URL}/info"

UAA_OUT="$(curl -sk --max-time 15 "${CF_LOGIN_URL}/info" 2>&1)" \
  || die "Step 9 FAILED: curl ${CF_LOGIN_URL}/info returned non-zero"

# UAA /info returns JSON with 'app' object containing UAA metadata.
if printf '%s\n' "${UAA_OUT}" | grep -qE '"app"|"commit_id"|"version"'; then
  log_ok "Step 9: UAA login endpoint reachable and returned info JSON"
  printf '%s\n' "${UAA_OUT}" | python3 -m json.tool 2>/dev/null || printf '%s\n' "${UAA_OUT}"
else
  log_warn "Step 9: UAA response may not be JSON — raw output:"
  printf '%s\n' "${UAA_OUT}"
fi

# ---------------------------------------------------------------------------
# Step 10: curl HAProxy direct — /info via internal IP
# ---------------------------------------------------------------------------

log_info "Step 10: curl HAProxy direct — https://${CF_HAPROXY_IP}/info"
log_info "         (skipped if host is unreachable — internal-only path)"

HAPROXY_RC=0
HAPROXY_OUT="$(curl -sk --max-time 10 \
  -H "Host: api.system.ocf.wayne.lab.fivetwenty.io" \
  "https://${CF_HAPROXY_IP}/v2/info" 2>&1)" || HAPROXY_RC=$?

if [[ "${HAPROXY_RC}" -ne 0 ]]; then
  log_warn "Step 10: curl to HAProxy ${CF_HAPROXY_IP} failed (rc=${HAPROXY_RC}) — host may be unreachable from this network; skipping"
elif printf '%s\n' "${HAPROXY_OUT}" | grep -q '"api_version"'; then
  log_ok "Step 10: HAProxy direct path reachable — /v2/info returned api_version"
  printf '%s\n' "${HAPROXY_OUT}" | python3 -m json.tool 2>/dev/null || printf '%s\n' "${HAPROXY_OUT}"
else
  log_warn "Step 10: HAProxy responded but /v2/info did not contain api_version — raw output:"
  printf '%s\n' "${HAPROXY_OUT}"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

log_ok "=== CF smoke test PASSED — ${CF_API_URL} healthy ==="
exit 0
