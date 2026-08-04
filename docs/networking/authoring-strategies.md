# Authoring Reserved-IP Strategies

This document is the reference for writing your own reserved-IP strategy definition and loading it into a bloc via `network.strategyPaths`. It assumes you have read [Reserved-IP Strategies](reserved-ip-strategies.md) — in particular sections 1–3 (the two-layer/two-tier model and the colocated/spanning placement concept) and section 9 (band overrides), which this document does not repeat.

A strategy definition is the same declarative YAML shape whether it ships as one of the three built-ins (`wide`, `compact`, `spanning`, embedded in the binary) or as an operator-supplied file loaded at config time — `internal/netlayout.LoadDefinition` and `Compile` never distinguish the two. Everything below applies equally to both.

## 1. Schema reference

A strategy definition is one YAML document with these top-level fields:

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `name` | string | yes | The catalog key operators select with `network.strategy`. Must not collide with an already-registered strategy's name (built-in or another BYO file) — see [Loading and precedence](#3-loading-and-precedence). |
| `description` | string | no | Free text, not read by any code path other than documentation tooling. |
| `scheme_version` | string | yes | The guard-stamped value written to a bloc's `reserved-ips` vault record. Must not collide with an already-registered strategy's `scheme_version` — see [Loading and precedence](#3-loading-and-precedence). |
| `placement` | string | yes | `colocated` or `spanning`. See [Reserved-IP Strategies §3](reserved-ip-strategies.md#3-placement-colocated-vs-spanning). |
| `min_prefix` | int | yes | The narrowest CIDR prefix (e.g. `25` for a `/25`) this strategy fits in. Every static offset and every closed band's `end` must fit within this prefix's last usable host offset, checked at compile time. |
| `min_subnets` | int | conditional | Forbidden (must be omitted) for `colocated` placement — the loader forces it to `1`. Required, and must be `>= 2`, for `spanning` placement. This is the number of workload subnets `ValidateSubnetSet` requires and the upper bound `subnets:` pins on statics/bands are checked against. |
| `tiers.mgmt` | mapping | conditional | A `TierDef` (below). At least one of `tiers.mgmt`/`tiers.ocf` is required. |
| `tiers.ocf` | mapping | conditional | A `TierDef` (below). |

A `TierDef` (the body of `tiers.mgmt` or `tiers.ocf`) has two fields:

| Field | Type | Notes |
| --- | --- | --- |
| `statics` | mapping of role name → static placement | Each key is the role name (`bastion`, `bosh`, `haproxy`, or any custom role your kits read a `<role>_ip` key from). See the two static forms below. |
| `available` | one band mapping, or a list of them | The tier's dynamic-allocation window(s). See the two band forms below. The reserved complement is ALWAYS derived from `available` at compile time — a definition never states its own reserved range. |

### Static placement — two accepted forms

A bare integer is the short form: the offset, unpinned (present on every subnet index), with the default `<role>_ip` output key.

```yaml
tiers:
  mgmt:
    statics:
      vault: 5
```

The mapping form adds pinning and/or a custom output key:

```yaml
tiers:
  mgmt:
    statics:
      doomsday:
        offset: 18
        subnets: [1]              # omit for "every index" (same as the bare form)
        ip_key: doomsday_ip       # omit to default to "<role>_ip"
      rustfs_smoke:
        offset: 21
        ip_key: rustfs_ip_smoke   # a custom key with no pinning — subnets omitted
```

Rules a static must satisfy (checked by `validateStatics`/`validateSubnetPin`, both called from `Compile`):

- `offset` must be `>= 3` (offsets 0–2 are the network/gateway addresses; offset floor is `ErrBadPinning`, not a separate sentinel).

- `subnets`, when set, is forbidden under `colocated` placement — pinning is `spanning`-only.

- Every index named in `subnets` must fall in `[0, min_subnets)`.

- `ip_key` is only accepted when `subnets` is unset (unpinned). A pinned static's compiled form never carries an `ip_key` — the engine's per-subnet-mapping code path does not read it, so setting both is rejected at validation time rather than silently dropped.

### Band placement — two accepted forms

A single mapping is the short form (equivalent to `wide`'s and `compact`'s single-band tiers):

```yaml
tiers:
  mgmt:
    available: { start: 32, end: 63 }
```

A list is accepted for multiple/pinned bands (`spanning`'s style, even though `spanning` itself only uses a single unpinned band per tier):

```yaml
tiers:
  ocf:
    available:
      - { start: 96, end: 127, subnets: [0] }
      - { start: 96, end: 127, subnets: [1] }
      - { start: 200, subnets: [2] }   # end omitted: open-ended, closes at the subnet's last usable host offset
```

Fields:

| Field | Type | Notes |
| --- | --- | --- |
| `start` | int | The band's first offset. |
| `end` | int | The band's last offset. Omit (or `0`) for an open-ended band that extends to the subnet's last usable host offset. |
| `subnets` | list of int | Omit for "every index". Same range/placement rules as a static's `subnets` (forbidden under `colocated`, each index in `[0, min_subnets)`). |

Rules a band must satisfy (checked by `validateBands`, then the per-index rules in `validateIndices`, both called from `Compile`):

- A closed band (`end > 0`) must have `start < end`.

- Every subnet index must resolve to EXACTLY ONE band per tier — zero or more than one covering the same index is rejected (`ErrBandOverlap`). This is why `spanning`'s example above needs three band entries (one per index) even though they share the same `[96,127]` range for indices 0–1: a single unpinned `{start:96,end:127}` entry would already cover every index, and adding a third pinned entry for index 2 would make indices 0–1 covered twice.

- On every subnet index, the mgmt tier's resolved band and the ocf tier's resolved band must not overlap (`ErrBandOverlap`, cross-tier).

- On every subnet index, no static from EITHER tier may fall inside either tier's resolved band — except the `ocf` tier's `haproxy` static, which is checked separately (below).

- On every subnet index, there may be at most one open-ended band across both tiers, and it must be the topmost zone: its `start` must sit above every closed band's `end` and every non-haproxy static's offset on that index (`ErrBandOverlap`).

- If the `ocf` tier declares a `haproxy` static, it must sit at exactly `(ocf band's start on that index) + 1` — the offset the Cloud Foundry kit's cloud-config hook assumes when it claims its own static window from inside the available band (`ErrHaproxyCoupling`; see [Reserved-IP Strategies §4](reserved-ip-strategies.md#4-the-wide-strategy) for why). A strategy with no `haproxy` static, or no `ocf` tier at all, is exempt.

## 2. Validation-rule catalog

Every rule below runs at one of three points: **load** (`LoadDefinition`, structural — runs on the YAML alone), **compile** (`Compile` → `validateDefinition`, semantic — runs once per definition when the catalog is built), or **catalog build** (`Catalog.add`, runs when a definition is registered alongside every other strategy already in the catalog). Band-OVERRIDE validation (`Layout.ValidateBand`) is a fourth, separate point — it runs per-bootstrap-call against a config-supplied override, not against the definition itself; see [Reserved-IP Strategies §9](reserved-ip-strategies.md#9-band-overrides).

All sentinels below live in `internal/netlayout` and are matched with `errors.Is`.

### Load-time (structural)

| Sentinel | Trigger | Example message |
| --- | --- | --- |
| `ErrInvalidDefinition` | YAML does not parse | `netlayout: invalid strategy definition: byo/custom.yaml: parse: ...` |
| `ErrInvalidDefinition` | `name` empty | `netlayout: invalid strategy definition: byo/custom.yaml: name is required` |
| `ErrInvalidDefinition` | `scheme_version` empty | `...: strategy "custom": scheme_version is required` |
| `ErrInvalidDefinition` | `min_prefix` is `0` (unset) | `...: strategy "custom": min_prefix is required` |
| `ErrInvalidDefinition` | `min_subnets` set under `colocated` | `...: strategy "custom": min_subnets is forbidden for colocated placement` |
| `ErrInvalidDefinition` | `min_subnets < 2` under `spanning` | `...: strategy "custom": spanning placement requires min_subnets >= 2` |
| `ErrInvalidDefinition` | `placement` is neither `colocated` nor `spanning` | `...: strategy "custom": placement must be "colocated" or "spanning", got "wide"` |
| `ErrInvalidDefinition` | neither `tiers.mgmt` nor `tiers.ocf` present | `...: strategy "custom": at least one tier is required` |

### Compile-time (semantic)

| Sentinel | Trigger | Example message |
| --- | --- | --- |
| `ErrBadPinning` | a static's `offset < 3` | `netlayout: invalid subnet pinning: byo/custom.yaml: strategy "custom" tier "mgmt" static "bastion": offset 1 below 3` |
| `ErrBadPinning` | a pinned static also sets `ip_key` | `...: strategy "custom" tier "mgmt" static "doomsday": ip_key is only supported on unpinned statics` |
| `ErrBadPinning` | `subnets` set under `colocated` placement | `...: strategy "custom" tier "mgmt" static "bastion": subnet pinning forbidden under colocated placement` |
| `ErrBadPinning` | a pinned index outside `[0, min_subnets)` | `...: strategy "custom" tier "mgmt" static "doomsday": subnet index 5 out of range [0,3)` |
| `ErrBandOverlap` | a closed band's `start >= end` | `netlayout: band overlap or coverage error: byo/custom.yaml: strategy "custom" tier "ocf" band[0]: start 100 must be less than end 90` |
| `ErrPrefixTooNarrow` | a static offset exceeds `min_prefix`'s last usable offset | `netlayout: min_prefix does not fit the definition's highest offset: byo/custom.yaml: strategy "custom" min_prefix /26 allows offsets up to 62, tier "ocf" static "haproxy" is at 97` |
| `ErrPrefixTooNarrow` | a closed band's `end` exceeds `min_prefix`'s last usable offset | `...: tier "mgmt" band[0] ends at 200` |
| `ErrOffsetCollision` | two statics (either tier) resolve to the same offset on one index | `netlayout: static offset collision: byo/custom.yaml: strategy "custom" index 0: offset 5 claimed by both mgmt/vault and ocf/vault` |
| `ErrBandOverlap` | an index is covered by zero or more than one band in one tier | `byo/custom.yaml: strategy "custom" tier "ocf" index 2: netlayout: band overlap or coverage error: subnet index 2 covered by 0 bands, want exactly 1` |
| `ErrBandOverlap` | mgmt's and ocf's resolved bands overlap on an index | `netlayout: band overlap or coverage error: byo/custom.yaml: strategy "custom" index 0: mgmt band [32,63] overlaps ocf band [50,90]` |
| `ErrBandOverlap` | a static falls inside a tier's resolved band on an index | `...: strategy "custom" index 1: tier "mgmt" static "doomsday" at 40 falls inside tier "mgmt" band [32,63]` |
| `ErrHaproxyCoupling` | `haproxy`'s offset ≠ its resolved ocf band start + 1 | `netlayout: haproxy must sit at ocf band start + 1 (...): byo/custom.yaml: strategy "custom" index 0: haproxy at 90, ocf band starts at 96 so the cf kit needs it at 97` |
| `ErrBandOverlap` | more than one open-ended band resolves to one index | `...: strategy "custom" index 0: 2 open-ended bands, want at most 1` |
| `ErrBandOverlap` | an open band is not the topmost zone (below a closed band's end) | `...: strategy "custom" index 0: open band must be the topmost zone (start 90 not above tier "mgmt" band end 95)` |
| `ErrBandOverlap` | an open band is not the topmost zone (below a static) | `...: strategy "custom" index 0: open band must be the topmost zone (start 90 not above tier "ocf" static "bosh" at 95)` |

### Band-OVERRIDE validation (`Layout.ValidateBand`, config-supplied, not part of a definition)

| Sentinel | Trigger | Example message |
| --- | --- | --- |
| `ErrBandOverridePartial` | only one of `start`/`end` is set | `netlayout: band override start and end must both be set, or neither` |
| `ErrBandOverrideStartTooLow` | `infra`-tier override `start < 12` | `netlayout: band override start collides with reserved named-IP slots: got 8, must be >= 12` |
| `ErrBandOverrideEndNotAfterStart` | `end <= start` | `netlayout: band override end must be greater than start: start=40 end=40` |
| `ErrBandOverrideEndBeyondSubnet` | `end` beyond the subnet's last usable offset | `netlayout: band override end is beyond the subnet's usable address range: end=2000 last-usable-offset=1022 subnet=10.0.0.0/22` |
| `ErrInvalidCIDR` | `cidr` does not parse as IPv4 | `netlayout: invalid CIDR` |
| `ErrBandOverrideCollidesStatic` | mgmt/ocf override range contains a named static (either tier, any index) | `netlayout: band override collides with a named static: [30,50] collides with tier "mgmt" static "shield" at offset 9` |
| `ErrBandOverrideCrossTier` | mgmt/ocf override range intersects the OTHER tier's own band (any index) | `netlayout: band override intersects the other tier's available band: tier "mgmt" override [50,120] intersects tier "ocf" band [96,1022]` |

### Catalog-build-time (`network.strategyPaths` loading)

| Sentinel | Trigger | Example message |
| --- | --- | --- |
| `ErrStrategyShadowed` | a BYO `name` matches an already-registered strategy (built-in or earlier BYO file) | `netlayout: strategy name conflicts with an already-registered strategy: strategy "wide" from byo/custom.yaml conflicts with an already-registered strategy` |
| `ErrSchemeCollision` | a BYO `scheme_version` matches an already-registered strategy's | `netlayout: scheme_version conflicts with an already-registered strategy: strategy "custom"'s scheme_version "2" already claimed by strategy "wide"` |

### Subnet-fit and subnet-count (`Layout.ValidateSubnet`/`ValidateSubnetSet`, and unknown-strategy lookup)

| Sentinel | Trigger | Example message |
| --- | --- | --- |
| `ErrSubnetTooSmall` | a workload CIDR's prefix is longer (narrower) than `min_prefix` | `subnet too small for strategy: strategy "custom" cidr "10.20.30.0/26" is /26, requires minimum /25 (highest fixed offset 97)` |
| `ErrTooFewSubnets` | fewer workload CIDRs given than `min_subnets` | `netlayout: too few workload subnets for strategy: strategy "custom" requires at least 3 workload subnets, got 1` |
| `ErrUnknownStrategy` | `network.strategy` names a strategy not in the bloc's catalog | `unknown network strategy "cuustom": known strategies are compact, custom, spanning, wide` |
| `ErrUnknownRole` | `LayerASlots` asked for a role other than `infra`/`ocfp` | `unknown netlayout role "bogus": known roles are "infra", "ocfp"` |
| `ErrNoMgmtTier` | `LayerASlots("ocfp", ...)` asked of a definition with no `tiers.mgmt` | `netlayout: strategy defines no mgmt tier: strategy "ocf-only" cannot answer the "ocfp" role` |

## 3. Loading and precedence

A bloc's strategy catalog is built once, at config load, from `network.strategyPaths` (or its snake_case alias `strategy_paths`):

```yaml
network:
  strategy: my-custom-triple
  strategyPaths:
    - strategies/my-custom-triple.yaml
    - strategies/shared/            # every *.yml/*.yaml in this dir, sorted
```

Precedence rules:

- `strategyPaths` (camelCase) wins over `strategy_paths` (snake_case) when BOTH are non-empty on the same `network:` block — the same precedence `network.strategy`/`network_strategy` already follow.

- A relative entry resolves against the directory containing the bloc's own config file, not the process's working directory.

- An entry naming a single file loads exactly that file as one strategy definition.

- An entry naming a directory loads every `*.yml`/`*.yaml` file directly inside it (non-recursive), in sorted filename order — deterministic regardless of the filesystem's own directory-listing order.

- The built-ins (`wide`, `compact`, `spanning`) are always in the catalog; `strategyPaths` only ADDS to that set, never replaces it.

- Loading stops at the first file that fails structural validation (`LoadDefinition`), semantic validation (`Compile`), or catalog registration (`ErrStrategyShadowed`/`ErrSchemeCollision`) — a bloc with an invalid BYO strategy file fails config load entirely, even if `network.strategy` selects a different, valid strategy. This is deliberate: a catalog that silently dropped a broken file could let an operator believe a strategy exists when its name actually resolves to nothing (or, worse, to whatever built-in happens to share its name).

`network.strategy`/`network_strategy` is resolved against this catalog by `(*Config).ResolveReservedIPLayout` — explicit name when set, else the provider/subnet-strategy default (`netlayout.DefaultNameFor`, see [Reserved-IP Strategies §2](reserved-ip-strategies.md#2-strategy-selection)) — AFTER `strategyPaths` has been loaded, so a BYO strategy name is resolvable as both an explicit selection and (if you write a `DefaultNameFor`-style default into your own tooling around a bloc's provider) implicitly.

### No-shadowing and unique-scheme rules

Two strategies (built-in or BYO, in any combination) may never share a `name`: `Catalog.add` checks the catalog's existing key set before registering a new definition, and the FIRST collision it finds fails the whole `BuildCatalog` call — a later file never silently overrides an earlier one, or a built-in. Naming a BYO strategy `wide`, `compact`, or `spanning` is always rejected, as is a second BYO file reusing a name an earlier BYO file already claimed.

Two strategies may never share a `scheme_version` either, for the same reason a `name` collision is rejected: the guard-stamped value written to a bloc's `reserved-ips` record has to identify exactly one table. If two strategies claimed `scheme_version: "2"`, a drift-guard report reading that stamp back could not tell which strategy's offsets to compare the recorded addresses against. Pick a `scheme_version` that is obviously yours — e.g. prefix it with an organization or bloc identifier (`"acme-1"`, `"acme-triple-2"`) — so it can never collide with a future built-in either.

## 4. Guard and stamping interaction

A BYO strategy participates in the drift guard exactly like a built-in: `layout.SchemeVersion()` — whatever `scheme_version` your YAML declares — is the value stamped into a bloc's `reserved-ips` record and compared on every subsequent populate. Nothing about the guard's withhold/report/review flow (`ocfp vault reserved-ips status`, `ocfp vault reserved-ips migrate`, `ocfp vault populate --force-reallocate`) changes for a BYO strategy; see [Reserved-IP Strategies §10](reserved-ip-strategies.md#10-scheme-stamping-and-drift-guard) for that flow in full.

The practical consequence: if you edit a BYO strategy file after a bloc has already been populated under it, treat the edit exactly as you would a built-in's — adding a role or widening a band is safe (the guard forwards new keys); moving an existing role's offset or a band's bounds is an address-moving change to a live system, and you should bump `scheme_version` when you do it, the same discipline `internal/vault/pve_reserved_ips_golden_test.go`'s comment documents for the built-ins. A `scheme_version` bump on a BYO file is a plain string edit — there is no registration step beyond the file itself, since the catalog re-reads it from disk on every config load.

## 5. Complete worked example

This definition spans two `/24` workload subnets, demonstrating both static forms, both band forms (including a pinned-band list), and a pinned `haproxy`:

```yaml
name: custom-dual
description: >
  Example BYO strategy spanning two /24 workload subnets: bastion/bosh/haproxy
  pinned to subnet 0, blacksmith pinned to subnet 1, everything else shared.
scheme_version: "acme-dual-1"
placement: spanning
min_prefix: 24
min_subnets: 2

tiers:
  mgmt:
    statics:
      bastion:   { offset: 3, subnets: [0] }        # mapping form, pinned
      vault:     5                                   # bare form, unpinned
      artifacts:
        offset: 11
        ip_key: artifacts_ip_primary                 # mapping form, custom key, unpinned
    available:
      - { start: 32, end: 63, subnets: [0] }          # pinned-band list, one entry per index
      - { start: 32, end: 63, subnets: [1] }

  ocf:
    statics:
      bosh:       { offset: 64, subnets: [0] }
      blacksmith: { offset: 67, subnets: [1] }
      haproxy:    { offset: 97, subnets: [0] }        # must sit at (ocf band start on idx 0) + 1
    available:
      start: 96                                       # bare mapping form, unpinned, open-ended
```

What each choice demonstrates, and why it passes `Compile`:

- `bastion` is pinned to index 0 only — its `bastion_ip` key appears in subnet 0's `reserved-ips` record and nowhere else. Because `placement: spanning`, this is allowed; the same pin under `placement: colocated` would fail with `ErrBadPinning`.

- `vault` and `artifacts` are unpinned, so they appear identically (at their own subnet's base address) on both indices — exactly like every `wide`/`compact` static.

- `artifacts`'s `ip_key` is accepted because it is unpinned; giving `bastion` an `ip_key` too would fail with `ErrBadPinning` ("ip_key is only supported on unpinned statics"), since `bastion` is pinned.

- The mgmt `available` band is written as a two-entry list — one pinned to `[0]`, one to `[1]` — rather than a single unpinned entry, purely to demonstrate the list form; a bare `{start: 32, end: 63}` here would resolve identically, since both entries carry the same bounds. Every index still resolves to exactly one band, satisfying the "exactly one band per index" rule.

- `haproxy` is pinned to index 0, and offset 97 is exactly the ocf tier's band start (96, unpinned so it applies to index 0 too) plus 1 — satisfying `ErrHaproxyCoupling`. On index 1, `haproxy` is not placed at all (`placedOn` excludes it), so the coupling check does not apply there.

- `blacksmith` at offset 67 sits inside neither tier's band on either index (both bands start at 32/96 or above; 67 sits between them), so it never trips `ErrBandOverlap`'s static-inside-band check on the index it IS placed on, and is simply absent — not colliding with anything — on the index it is not.

- `min_prefix: 24` gives a last usable host offset of 254, comfortably above every static offset used (highest is 97) and every closed band end (63) — well within `ErrPrefixTooNarrow`'s limit.

Save this as (for example) `strategies/custom-dual.yaml`, reference it from a bloc's config:

```yaml
network:
  strategy: custom-dual
  strategyPaths:
    - strategies/custom-dual.yaml
```

and it becomes selectable exactly like `wide`, `compact`, or `spanning` — including in `ocfp vault reserved-ips status`'s catalog listing and in the `ErrUnknownStrategy` error's "known strategies" list for that bloc.

## See Also

- [Reserved-IP Strategies](reserved-ip-strategies.md) for the built-in strategies' own offset tables, the two-layer/two-tier model, band overrides, and scheme stamping
