#!/usr/bin/env bash
set -euo pipefail

# 02-env-bosh-smoke.sh — Verify wayne env-BOSH director health.
#
# Runs FROM the bastion (after `ocfp bastion ssh`).
# Requires: safe, bosh CLIs installed; vault unsealed and safe targeted.
#
# Vault path convention: /secret/exodus/<genesis-env-name>/bosh
# Genesis env: ocfp-pve-wayne-ocf  Director IP: 10.64.64.12

# ---------------------------------------------------------------------------
# Configuration
# ---------------------------------------------------------------------------

BOSH_ENV_NAME="${BOSH_ENV_NAME:-wayne-ocf}"
BOSH_DIRECTOR_IP="${BOSH_DIRECTOR_IP:-10.64.64.12}"
VAULT_EXODUS_BASE="${VAULT_EXODUS_BASE:-secret/exodus/ocfp-pve-wayne-ocf/bosh}"
RECENT_TASKS="${RECENT_TASKS:-5}"

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

log_info "=== env-BOSH smoke test — $(date -u '+%Y-%m-%dT%H:%M:%SZ') ==="
log_info "Director: ${BOSH_DIRECTOR_IP}  alias: ${BOSH_ENV_NAME}"
log_info "Vault exodus base: ${VAULT_EXODUS_BASE}"

for tool in safe bosh; do
  command -v "${tool}" >/dev/null 2>&1 || die "required tool not found on PATH: ${tool}"
done
log_ok "Required tools on PATH: safe bosh"

# ---------------------------------------------------------------------------
# Step 1: Resolve env-BOSH credentials from vault
# ---------------------------------------------------------------------------

log_info "Step 1: Resolving BOSH credentials from vault (${VAULT_EXODUS_BASE})"

BOSH_CA_CERT="$(safe get "${VAULT_EXODUS_BASE}:ca_cert" 2>/dev/null)" \
  || die "Step 1 FAILED: cannot read ca_cert from vault path ${VAULT_EXODUS_BASE}:ca_cert — is vault unsealed and safe targeted?"

[[ -n "${BOSH_CA_CERT}" ]] \
  || die "Step 1 FAILED: ca_cert is empty at ${VAULT_EXODUS_BASE}:ca_cert"

BOSH_CLIENT="$(safe get "${VAULT_EXODUS_BASE}:admin_username" 2>/dev/null)" \
  || die "Step 1 FAILED: cannot read admin_username from vault"

# admin_username may not be stored in all kits; fall back to 'admin'
[[ -n "${BOSH_CLIENT}" ]] || BOSH_CLIENT="admin"

BOSH_CLIENT_SECRET="$(safe get "${VAULT_EXODUS_BASE}:admin_password" 2>/dev/null)" \
  || die "Step 1 FAILED: cannot read admin_password from vault path ${VAULT_EXODUS_BASE}:admin_password"

[[ -n "${BOSH_CLIENT_SECRET}" ]] \
  || die "Step 1 FAILED: admin_password is empty at ${VAULT_EXODUS_BASE}:admin_password"

log_ok "Step 1: credentials resolved (user=${BOSH_CLIENT})"

# Export so bosh CLI sub-commands inherit them
export BOSH_CA_CERT
export BOSH_CLIENT
export BOSH_CLIENT_SECRET

# ---------------------------------------------------------------------------
# Step 2: Alias the director environment
# ---------------------------------------------------------------------------

log_info "Step 2: Aliasing director — bosh alias-env ${BOSH_ENV_NAME} -e ${BOSH_DIRECTOR_IP}"

# Write CA cert to a temp file; bosh alias-env reads it from --ca-cert flag.
# This avoids env-var quoting issues with multi-line PEM blocks.
CA_CERT_FILE="$(mktemp /tmp/env-bosh-ca-XXXXXX.pem)"
trap 'rm -f "${CA_CERT_FILE}"' EXIT

printf '%s\n' "${BOSH_CA_CERT}" > "${CA_CERT_FILE}"

bosh alias-env "${BOSH_ENV_NAME}" \
  -e "${BOSH_DIRECTOR_IP}" \
  --ca-cert "${CA_CERT_FILE}" \
  --non-interactive \
  >/dev/null \
  || die "Step 2 FAILED: bosh alias-env returned non-zero"

log_ok "Step 2: alias '${BOSH_ENV_NAME}' created for https://${BOSH_DIRECTOR_IP}:25555"

# ---------------------------------------------------------------------------
# Step 3: Authenticate
# ---------------------------------------------------------------------------

log_info "Step 3: Authenticating — bosh log-in -e ${BOSH_ENV_NAME}"

bosh log-in \
  -e "${BOSH_ENV_NAME}" \
  --non-interactive \
  >/dev/null \
  || die "Step 3 FAILED: bosh log-in returned non-zero — check credentials"

log_ok "Step 3: authenticated as ${BOSH_CLIENT}"

# ---------------------------------------------------------------------------
# Step 4: Director info
# ---------------------------------------------------------------------------

log_info "Step 4: Director info — bosh -e ${BOSH_ENV_NAME} env"

bosh -e "${BOSH_ENV_NAME}" env --non-interactive \
  || die "Step 4 FAILED: bosh env returned non-zero"

log_ok "Step 4: director info OK"

# ---------------------------------------------------------------------------
# Step 5: VMs
# ---------------------------------------------------------------------------

log_info "Step 5: VMs across all deployments — bosh -e ${BOSH_ENV_NAME} vms"

bosh -e "${BOSH_ENV_NAME}" vms --non-interactive \
  || die "Step 5 FAILED: bosh vms returned non-zero"

log_ok "Step 5: vms OK"

# ---------------------------------------------------------------------------
# Step 6: Deployments
# ---------------------------------------------------------------------------

log_info "Step 6: Deployments — bosh -e ${BOSH_ENV_NAME} deployments"

bosh -e "${BOSH_ENV_NAME}" deployments --non-interactive \
  || die "Step 6 FAILED: bosh deployments returned non-zero"

log_ok "Step 6: deployments OK"

# ---------------------------------------------------------------------------
# Step 7: Stemcells
# ---------------------------------------------------------------------------

log_info "Step 7: Stemcells — bosh -e ${BOSH_ENV_NAME} stemcells"

STEMCELL_OUT="$(bosh -e "${BOSH_ENV_NAME}" stemcells --non-interactive 2>&1)" \
  || die "Step 7 FAILED: bosh stemcells returned non-zero: ${STEMCELL_OUT}"

printf '%s\n' "${STEMCELL_OUT}"

# Warn (not fail) if no stemcells yet — director is freshly deployed.
# A CF-compatible stemcell (Jammy/Noble) must be uploaded before deploying CF.
if printf '%s\n' "${STEMCELL_OUT}" | grep -qi "0 stemcells\|no stemcells"; then
  log_warn "Step 7: no stemcells uploaded yet — upload a Jammy/Noble stemcell before deploying CF"
else
  log_ok "Step 7: stemcells OK"
fi

# ---------------------------------------------------------------------------
# Step 8: Releases
# ---------------------------------------------------------------------------

log_info "Step 8: Releases — bosh -e ${BOSH_ENV_NAME} releases"

bosh -e "${BOSH_ENV_NAME}" releases --non-interactive \
  || die "Step 8 FAILED: bosh releases returned non-zero"

log_ok "Step 8: releases OK"

# ---------------------------------------------------------------------------
# Step 9: Recent tasks
# ---------------------------------------------------------------------------

log_info "Step 9: Recent tasks (last ${RECENT_TASKS}) — bosh -e ${BOSH_ENV_NAME} task --recent ${RECENT_TASKS}"

bosh -e "${BOSH_ENV_NAME}" task --recent "${RECENT_TASKS}" --non-interactive \
  || die "Step 9 FAILED: bosh task --recent returned non-zero"

log_ok "Step 9: recent tasks OK"

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

log_ok "=== env-BOSH smoke test PASSED — director ${BOSH_DIRECTOR_IP} healthy ==="
exit 0
