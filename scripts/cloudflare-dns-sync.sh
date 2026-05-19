#!/usr/bin/env bash
set -euo pipefail

: "${CLOUDFLARE_API_TOKEN:?set CLOUDFLARE_API_TOKEN (Zone:DNS:Edit on fivetwenty.io)}"

CONFIG="${OCFP_CONFIG:-$HOME/.ocfp/config.pve.yml}"
ZONE="${CLOUDFLARE_ZONE:-fivetwenty.io}"

if [ ! -f "$CONFIG" ]; then
  echo "config not found: $CONFIG" >&2
  exit 1
fi

ZONE_ID=$(curl -fsS -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
  "https://api.cloudflare.com/client/v4/zones?name=$ZONE" \
  | jq -r '.result[0].id // empty')

if [ -z "$ZONE_ID" ]; then
  echo "zone $ZONE not found or token lacks access" >&2
  exit 1
fi

upsert_a() {
  local name=$1 ip=$2
  local existing_id body
  existing_id=$(curl -fsS -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
    "https://api.cloudflare.com/client/v4/zones/$ZONE_ID/dns_records?type=A&name=$name" \
    | jq -r '.result[0].id // empty')
  body=$(jq -n --arg n "$name" --arg ip "$ip" \
    '{type:"A",name:$n,content:$ip,ttl:60,proxied:false}')
  if [ -n "$existing_id" ]; then
    curl -fsS -X PUT -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
      -H "Content-Type: application/json" -d "$body" \
      "https://api.cloudflare.com/client/v4/zones/$ZONE_ID/dns_records/$existing_id" >/dev/null
    echo "updated $name -> $ip"
  else
    curl -fsS -X POST -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
      -H "Content-Type: application/json" -d "$body" \
      "https://api.cloudflare.com/client/v4/zones/$ZONE_ID/dns_records" >/dev/null
    echo "created $name -> $ip"
  fi
}

mapfile -t BLOCS < <(yq -r '.blocs | keys | .[]' "$CONFIG" | grep '^ocfp-pve-' || true)
if [ "${#BLOCS[@]}" -eq 0 ]; then
  echo "no ocfp-pve-* blocs in $CONFIG" >&2
  exit 1
fi

for bloc in "${BLOCS[@]}"; do
  base=$(yq -r ".blocs.\"$bloc\".fqdns.base // empty" "$CONFIG")
  [ -n "$base" ] || { echo "skip $bloc — no fqdns.base"; continue; }

  ts_ip=$(tailscale status --json \
    | jq -r ".Peer[]? | select(.HostName == \"$bloc-bastion\") | .TailscaleIPs[0] // empty")
  [ -n "$ts_ip" ] || { echo "skip $bloc — no tailscale IP for $bloc-bastion"; continue; }

  upsert_a "$base" "$ts_ip"
  upsert_a "*.$base" "$ts_ip"
done
