# Endpoints

`ocfp endpoints` (aliases `dns`, `domains`) lists every DNS-relevant
hostname and IP fact for a bloc in one report: derived service FQDNs,
Cloudflare service routes, ingress records, and the bastion. It is
read-only and safe to run against any bloc at any time.

## Usage

```bash
ocfp --bloc example endpoints
ocfp --bloc example endpoints --output json
ocfp --bloc example endpoints --no-resolve
```

`endpoints` takes no positional arguments and has no child subcommands.
It uses the global `--bloc` and `--config` flags to select and load the
bloc, plus two of its own:

| Flag | Description | Default |
|---|---|---|
| `--output` | Output format: `table`, `json`, or `yaml` | `table` |
| `--no-resolve` | Skip live DNS lookups; every RESOLVED IP cell stays blank | `false` |

## What it never does

`endpoints` reads only the bloc's local config file and local state file
on disk, plus, unless `--no-resolve` is given, live DNS lookups for the
hostnames it collects. It never calls Vault, never calls a cloud
provider API, and never shells out to the `tailscale` CLI. See
"Vault is never consulted" below for why that specific omission matters
operationally.

## The four sections

Each section answers a different question, and rows are not
deduplicated across sections. The only cross-section behavior is that
every unique, non-wildcard hostname collected from any section is
resolved once and the result is reused wherever that same hostname
appears.

### 1. Derived Service FQDNs

Columns: `ENV`, `SERVICE`, `FQDN`, `EXPECTED IP`, `ORIGIN`, `RESOLVED IP`.

One row per known service for both the `mgmt` and `ocf` environment
tiers, plus any service that only has an explicit FQDN override, showing
each service's derived or explicit FQDN alongside its EXPECTED and
ORIGIN facts.

| ENV | SERVICE | FQDN | EXPECTED IP | ORIGIN | RESOLVED IP |
|---|---|---|---|---|---|
| mgmt | concourse | concourse.system.ocf.example.io | 10.0.0.21 | — | 10.0.0.21 |
| ocf | shield | shield.system.ocf.example.io | 10.0.0.20 | 10.0.0.9 | 10.0.0.9 |

`concourse`'s ORIGIN is blank: nothing in `cloudflare.services` has a
hostname that exactly matches `concourse.system.ocf.example.io`.
`shield`'s ORIGIN is populated because its derived name suffix-matches
the bloc's configured system domain, so it falls through to the shared
CF haproxy address. Both rows still show a RESOLVED IP, because DNS for
both names happens to be configured, independent of whether ORIGIN found
a match.

### 2. Cloudflare Service Routes

Columns: `KIND`, `HOSTNAME`, `SERVICE URL`, `ORIGIN`, `RESOLVED IP`.

One row per configured Cloudflare route: the `*.apps` and `*.system`
wildcards, the SSH route when configured, and every entry under
`cloudflare.services`.

| KIND | HOSTNAME | SERVICE URL | ORIGIN | RESOLVED IP |
|---|---|---|---|---|
| apps wildcard | *.apps.ocf.example.io | https://10.0.0.9 | 10.0.0.9 | — |
| service | grafana.example.io | https://10.0.0.22 | 10.0.0.22 | 10.0.0.22 |

This section has no EXPECTED IP column. A plain DNS lookup of any
`HOSTNAME` here never directly returns the origin IP: under the
Cloudflare tunnel it returns a CNAME to the tunnel, and under the
tailscale ingress provider no per-service DNS record is ever created for
these hostnames at all. Adding an EXPECTED IP column here would assert a
fact that is never true, so it is left out entirely rather than kept as
a column that renders blank on every row it could ever have.

### 3. Ingress Records

Columns: `RECORD`, `TYPE`, `EXPECTED TARGET`, `ORIGIN`, `RESOLVED IP`.

The DNS record set the bloc's ingress provider is responsible for. The
row set depends on which ingress provider is resolved for the bloc
(`cloudflared`, `tailscale`, or neither); see
[Ingress Providers](ingress-providers.md) for how that resolution works
and what each provider's data path looks like end to end.

`EXPECTED TARGET` is always `—` on every row in this section, for both
providers. This is by design, not a gap the command intends to close
later:

- On a tailscale bloc, the true expected target is the bastion's
  tailnet IP address. Getting that value requires a local `tailscale`
  CLI shell-out, which this command never performs.

- On a cloudflared bloc, the true expected target is the tunnel's
  `<tunnel-id>.cfargotunnel.com` hostname. Getting that value requires a
  Vault read, which this command never performs (see "Vault is never
  consulted" below).

Both the row set and the `ORIGIN` value differ by provider:

- Under `tailscale`, the rows are the apex `fqdns.base` and its `*.`
  wildcard. Both take ORIGIN from the bloc's configured Cloudflare
  origin address, which remains the true destination once traffic
  reaches the CF haproxy even when the tunnel is disabled — the
  bastion's own DNAT rule delivers it there instead.

- Under `cloudflared`, there is no apex row. The `*.apps` and
  `*.system` wildcard rows take ORIGIN from that same configured
  Cloudflare origin address.

- Also under `cloudflared`, the SSH row and each `cloudflare.services`
  row instead carry their own per-route origin, read from that route's
  configured service URL — the same value Section 2 reports for the
  same hostname. A per-route origin can be an entirely different
  address than the bloc's Cloudflare origin, so do not read every cell
  in this column as the tunnel or DNAT address.

### 4. Bastion

Columns: `NAME`, `VALUE`.

The bastion's allocated IP, always present, plus its tailnet hostname
when the resolved ingress provider is tailscale. No ORIGIN or RESOLVED
IP column, and no network activity: the bastion is the entry point
itself, not a backend sitting behind a wildcard, and its tailnet
hostname is a local config-derived value.

## Column semantics

Every hostname-bearing row across sections 1 through 3 can carry up to
three independent facts. They may or may not agree with each other, and
the command does no cross-checking or drift-flagging between them —
disagreement between the three is the signal for a human reading the
output to investigate, not an error the command raises itself.

- **EXPECTED IP**
  the address this bloc's own resource inventory reserved for a role: a
  fact about allocation, independent of how traffic currently reaches
  it. Sourced from local reserved-IP state for the role, or the
  bastion's configured IP for the bastion row. Never derived from
  Cloudflare route configuration.

- **ORIGIN**
  where traffic for this hostname terminates today, given the bloc's
  actual ingress wiring. It is always the bare host of some configured
  origin URL; what differs between sections is how that URL gets
  selected.

  Section 1 is the only section that matches anything. A derived FQDN
  takes the origin of a Cloudflare service or SSH route whose hostname
  equals it exactly; failing that, an OCF-tier hostname — and only an
  OCF-tier one — may fall through to the CF haproxy address behind the
  `*.apps`/`*.system` wildcard. Matching is exact string equality,
  deliberately, with no heuristic, fuzzy, or service-name-based
  matching. See "Why ORIGIN is usually blank" below.

  Sections 2 and 3 match nothing. Each row there already corresponds to
  one configured route or record, so it shows that route's own origin
  directly, and its ORIGIN cell is blank only when the underlying
  config field is unset.

- **RESOLVED IP**
  what a live DNS lookup returns right now, or blank under
  `--no-resolve`, or blank for a wildcard or empty hostname that cannot
  be looked up at all. Only the first address the resolver returns is
  shown. For a dual-stack name that can be the IPv6 answer, while
  EXPECTED IP is always IPv4 — so those two columns can disagree for a
  purely cosmetic reason, with nothing actually misconfigured.

## Why ORIGIN is usually blank

A blank ORIGIN cell means "no configured route matches this exact
name." It does not mean "no route exists" for that service.

This is the common case, not the exceptional one. A real bloc routinely
carries up to three unreconciled names for the same service at once:

- the plain name the command derives from the service and the bloc's
  base domain

- an explicit FQDN override configured for that service

- a hostname configured under a Cloudflare service route for that same
  service

Nothing in the configuration model requires any two of these three
names to match, and this command makes no attempt to reconcile them:
matching is exact-string-only, by design. An operator who wants a
service's route and its derived name to agree has to keep the
configuration consistent themselves; the command reports the
mismatch as a blank cell rather than guessing at an intended
connection.

Mgmt-tier services never populate ORIGIN through the wildcard fallback
described above, even when the bloc's Cloudflare origin and system
domain are both configured. Only OCF-tier services sit behind the CF
haproxy's `*.apps`/`*.system` wildcard; a mgmt-tier service can still
show a populated ORIGIN, but only when it has its own explicit
Cloudflare service route.

## Vault is never consulted

`endpoints` derives every FQDN it shows from the bloc's local
configuration file alone. It never reads Vault, and never reads the
BOSH manifests that have actually been deployed.

This has a real operational consequence, and it is the single most
important thing to understand before acting on this command's output.

FQDN derivation for a subset of infra-UI services depends on whether the
bloc has an ingress provider configured. A bug in that derivation logic
was fixed after this command was already designed: blocs whose ingress
was misclassified derived the wrong, bare-form hostname for those
services instead of the corrected form. That fix changes what this
command derives and displays going forward. It does not retroactively
change what is already sitting in Vault for a bloc that was provisioned
before the fix, and it does not change what the currently deployed BOSH
manifests for that bloc's kits actually contain.

In practice, this means: for a bloc that has not yet been re-provisioned
since the fix, `ocfp endpoints` shows the corrected hostname, while
Vault and the deployed kits still serve the old one, until an operator
re-runs FQDN provisioning for that bloc (`ocfp vault populate`) and
redeploys the kits that consume the affected FQDNs.

**Do not read a corrected FQDN in this command's output as evidence
that a bloc has already been fixed.** This command shows what the
configuration says should be true. It does not show what is currently
deployed. Confirm the actual deployed state (Vault contents, or the
running kit's own configuration) before treating a bloc as reconciled.

## See Also

- [Ingress Providers](ingress-providers.md) for how the tailscale and
  cloudflared ingress providers are resolved and how each one's data
  path works end to end

- [Cloudflare DNS Sync](dns-cloudflare-sync.md) for the wildcard DNS
  sync this command's Cloudflare section reports on

- [Public IPs](public-ips.md) for how the bastion and other reserved
  addresses this command's EXPECTED IP column reads are allocated

- [Networking Overview](README.md) for the provider support matrix
