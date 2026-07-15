# Artifacts TLS trust contract

This page is the contract for TLS on the ocfp-artifacts VM: the three
`artifacts.tls.mode` values, how the certificate material is generated and
stored, how every consumer (bastion, `aws` CLI, `scripts/blobstores`,
BOSH/CF kits, and operators) resolves trust, and how to rotate certificates
when they expire or the CA needs replacing.

## The three modes

`artifacts.tls.mode` (`docs/config.pve.example.yml`) selects one of:

| Mode | Certificate source | Default | Vault required |
|---|---|---|---|
| `internal-ca` | Leaf issued from a per-bloc CA minted on demand at `secret/ocfp/{bloc}/ca` | Yes | Yes, at bootstrap (auto-started if unreachable) |
| `self-signed` | RustFS's own self-signed leaf, no shared CA | No | No |
| `disabled` | Plain HTTP, no TLS | No | No |

`internal-ca` is the default (`internal/config/artifacts_config.go`) because
the CA is generated automatically the first time it is needed — there is no
manual PKI setup step. Blocs that cannot reach vault at bootstrap time (no
tmux inception vault, no `VAULT_ADDR`/`VAULT_TOKEN`, no `safe target`) should
set `tls.mode: self-signed` explicitly; `self-signed` remains fully
supported and has no vault dependency.

Day-2 operations (status, provision, teardown, skip-path convergence) branch
on the **state-recorded** `tls_mode` for an already-deployed VM, not on the
current config value, so changing the config default never affects a bloc
that has already been bootstrapped.

### Upgrading existing blocs: self-signed now fails closed in scripts/blobstores

`scripts/blobstores` used to fall back to `--no-verify-ssl` silently for
any `https` endpoint it couldn't verify. It now verifies by default and
fails closed (exit 3) instead. This is a real behavior change for
existing `self-signed`-mode blocs specifically: the bastion trust store
has nothing to install for `self-signed` (`bloc_ca_trust` is a deliberate
no-op outside `internal-ca`), and `secret/ocfp/{bloc}/ca` is never
written in `self-signed` mode either, so both of the script's CA
resolution tiers come up empty and it has no CA left to fall back to.

Existing `self-signed` blocs have two options:

- Pass `--insecure` to `scripts/blobstores` (and `--no-verify-ssl`/`-k` to
  any manual `aws`/`curl` command) to keep the old skip-verify behavior.
- Migrate to `internal-ca`: set `artifacts.tls.mode: internal-ca` in the
  bloc config and re-run `ocfp artifacts provision --bloc <bloc>`. This is
  the recommended path — it mints the bloc CA, re-issues the leaf from
  it, and every consumer above starts verifying without any `--insecure`
  flags anywhere.

## CA lifecycle

The bloc CA is minted once per bloc and reused for the life of the bloc:

- **Algorithm**: ECDSA P-256.
- **Validity**: 10 years.
- **Subject**: `CN=ocfp-{bloc}-internal-ca`.
- **Key usage**: `CertSign | CRLSign`; `IsCA: true` with a critical
  `BasicConstraints`.
- **Storage**: `secret/ocfp/{bloc}/ca`, with fields `cert` (PEM), `key`
  (PEM), `fingerprint` (SHA-256 hex of the DER certificate), and
  `created_at` (RFC 3339).

Two vault-side entry points exist for this material:

- `LoadOrGenerateBlocCA` — mints the CA on first use and is idempotent
  afterward (repeated calls return the same cert/key, never regenerate).
  Used by bootstrap and `artifacts provision`.
- `LoadBlocCA` — read-only; returns `ErrBlocCANotFound` if the path is
  empty and never mints. Used by `ocfp artifacts ca`, the bastion trust
  install phase, and every other read-only trust-recovery path, so a
  read command can never have the side effect of silently creating trust
  material.

## Leaf lifecycle

Each artifacts VM gets its own leaf certificate signed by the bloc CA (or,
in `self-signed` mode, a self-signed leaf with no shared CA):

- **Algorithm**: ECDSA P-256.
- **Validity**: 1 year.
- **Subject**: the configured `tls.common_name`, or the VM hostname
  (`{bloc}-artifacts`) when unset.
- **SANs**: the CN, the VM hostname, the VM's SDN private IP, and the
  loopback addresses `127.0.0.1` and `::1`. The loopback SANs let
  anything running on the artifacts VM itself — the provisioning script,
  a local health check, RustFS's own bucket-creation step — pass full
  certificate verification against `https://127.0.0.1:9000`, not just the
  SDN address.
- **Key usage**: `DigitalSignature | KeyEncipherment`, `ExtKeyUsage:
  ServerAuth`.
- **Expiry tracking**: the leaf's `not_after` is recorded as
  `tls_leaf_not_after` (RFC 3339) in both the artifacts state resource and
  the `secret/ocfp/{bloc}/artifacts` vault metadata at issuance, and
  refreshed on every `ocfp artifacts provision` (the state write always
  happens; the vault write is best-effort and skipped, without failing the
  provision, when vault is unreachable). `ocfp artifacts status` prints the
  recorded `tls_leaf_not_after` and — when the endpoint is reachable —
  TLS-dials it and prints the live-probed leaf's own `not_after` alongside
  it, warning (naming days remaining, or `EXPIRED` if already past) when
  either value is within 30 days of expiry. The same live dial also reads
  the presented leaf's SHA-256 fingerprint and compares it against the
  pinned `tls_fingerprint_sha256` from state, flagging drift with a
  `tls_fingerprint_drift` warning (naming both fingerprints) when they
  don't match — a sign the VM is serving a leaf state/vault doesn't know
  about. Both checks are best-effort and status-only: an unreachable
  endpoint degrades to the recorded values with no live comparison, and
  drift is never treated as a trust failure or reflected in the command's
  exit code.
- **Bastion re-sync**: `ocfp vault populate` (run on the bastion, against
  the bastion's own inception vault — a vault instance the workstation-side
  writer above never touches) also re-writes `secret/ocfp/{bloc}/artifacts`
  whenever it auto-sources blobstore config from bootstrap state, so
  bastion-side tooling (`scripts/blobstores`) can discover the endpoint
  even when this bastion vault has never seen a workstation
  `ocfp artifacts provision` write.

## How each consumer gets trust

```mermaid
flowchart TD
    CA["Bloc CA\nsecret/ocfp/{bloc}/ca\n(cert, key, fingerprint)"]
    Leaf["Artifacts VM leaf cert\n(1yr, CN + SDN IP + loopback SANs)"]
    CA -- signs --> Leaf

    CA -- "ca_cert field" --> VaultBlobstores["Vault blobstore paths\n{bloc}/mgmt/bosh/blobstores/bosh\n{bloc}/ocf/bosh/blobstores/bosh\n{bloc}/ocf/cf/blobstores/main"]
    CA -- "bloc_ca_trust phase" --> Bastion["Bastion OS trust store\n/usr/local/share/ca-certificates/\nocfp-{bloc}-internal-ca.crt"]
    CA -- "ocfp artifacts ca" --> Operator["Operator workstation / CI"]

    VaultBlobstores -- "(( vault ... \":ca_cert\" ))" --> Kits["BOSH + CF genesis kits"]
    Bastion -- "AWS_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt" --> AWSCli["aws CLI v2"]
    Bastion -- "merged system bundle" --> Blobstores["scripts/blobstores helper"]
    CA -- "RUSTFS_TLS_CA env" --> ArtifactsVM["Artifacts VM own trust store\n/usr/local/share/ca-certificates/ocfp-ca.crt"]

    Leaf -- serves --> RustFS["RustFS S3 endpoint\nhttps://{sdn-ip}:9000"]
    ArtifactsVM -- "verified bucket creation" --> RustFS
```

- **Bastion trust store** — `ocfp bastion init` runs a `bloc_ca_trust`
  phase (registered in both the sequential and parallel-post phase lists in
  `internal/bastion/init.go`, so neither init mode skips it) that resolves
  the bloc CA cert operator-side (state's recorded `ca_cert` first, vault
  second), writes it to
  `/usr/local/share/ca-certificates/ocfp-{bloc}-internal-ca.crt`, and runs
  `update-ca-certificates`. Idempotent: it hashes the resolved PEM and
  compares against the already-installed file's checksum, skipping the
  write and the `update-ca-certificates` run when they match. This phase
  is a deliberate no-op (logged, not an error) when artifacts is disabled,
  `tls.mode` is not `internal-ca`, or no CA is resolvable, so a bastion
  re-init never fails outright over trust-store convergence alone.
- **`aws` CLI (v2)** — AWS CLI v2 ships its own certificate bundle and
  ignores the OS trust store entirely. `ocfp bastion init` exports
  `AWS_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt` in the bastion
  environment when artifacts TLS is on, pointing the CLI at the merged
  system bundle (bloc CA plus the normal public CA set, so `aws s3` against
  real AWS still works from the same shell).
- **`scripts/blobstores`** — verifies by default. CA resolution order:
  `--ca-bundle <path>` (explicit override), then the merged system bundle
  when the bastion-installed
  `/usr/local/share/ca-certificates/ocfp-{bloc}-internal-ca.crt` is
  present, then a `safe get secret/ocfp/{bloc}/ca:cert` fetch cached at
  `~/.ocfp/{bloc}/ca.pem`. `--insecure` (`aws --no-verify-ssl` / `curl -k`)
  is only used when none of those resolve and the flag is passed
  explicitly; otherwise the script fails pointing at
  `ocfp artifacts ca --bloc <bloc>`.
- **BOSH/CF genesis kits** — never touch the CA directly. `ArtifactsWriter`
  (`internal/vault/artifacts.go`) writes the CA cert into the `ca_cert`
  field of each blobstore config entry it fans out to
  (`{bloc}/mgmt/bosh/blobstores/bosh`, `{bloc}/ocf/bosh/blobstores/bosh`,
  `{bloc}/ocf/cf/blobstores/main`, plus their `/creds` siblings), so kits
  pin trust the same way they read the endpoint/host/port/bucket —
  through `(( vault ... ))` spruce operators against those paths.
- **`ocfp artifacts ca`** — manual/CI fetch path when no bastion trust
  store is available. Reads `secret/ocfp/{bloc}/ca` read-only (never
  mints unless `--generate` is passed) and prints the CA cert PEM to
  stdout, or writes it to a file with `--out`, or prints
  `--fingerprint`/`--json` (cert, fingerprint, `not_before`/`not_after`,
  `created_at`) for inspection.
- **Client-side code paths** (`ocfp precompile cf`, `ocfp vault populate`,
  and any other in-CLI S3 client) resolve trust through the single
  `artifacts.EndpointForLookup` helper: a CA cert already known (from
  state, or an explicit override) pins verification; an `https` endpoint
  with `tls.mode: internal-ca` and no CA cert triggers a live recovery via
  `LoadBlocCA` before erroring; `self-signed` with no CA cert requires an
  explicit insecure opt-in flag; verification is never silently skipped
  for `internal-ca`.
- **The artifacts VM itself** — `ocfp artifacts provision` delivers the
  trust material (CA cert for `internal-ca`, the leaf cert for
  `self-signed`) to the VM via the `RUSTFS_TLS_CA` environment variable,
  passed through both the SSH provisioning path and the cloud-init
  provisioning path. When set, `scripts/provision/artifacts` writes it to
  `/usr/local/share/ca-certificates/ocfp-ca.crt`, runs
  `update-ca-certificates`, and creates the BOSH/CF buckets with
  verified TLS (`--ca-bundle /etc/ssl/certs/ca-certificates.crt`) against
  its own `https://127.0.0.1:9000` endpoint — the loopback SANs on the
  leaf are what make that on-box verification possible. Older CLI builds
  that don't set `RUSTFS_TLS_CA` fall back to `--no-verify-ssl` for this
  one on-box step only; this fallback has no effect on how any other
  consumer verifies the endpoint from outside the VM.

## Fingerprint semantics

`tls_fingerprint_sha256` (written to `secret/ocfp/{bloc}/artifacts`, and
printed by `ocfp artifacts ca --fingerprint`) is **operator metadata
only** — a human-readable way to confirm "am I talking to the RustFS
instance I think I am" via `ocfp artifacts status` or a manual `openssl
s_client` comparison. It is never read back to make a trust decision.
Every TLS client in this codebase verifies against the full CA
certificate (`ca_cert` / `ep.CACert`), not against this fingerprint —
comparing a served cert's fingerprint against this value in place of
normal chain verification would pin to a single leaf certificate, which
breaks on every routine leaf rotation.

`ocfp artifacts status` automates exactly this human comparison: it
live-probes the endpoint's served fingerprint and compares it against the
pinned `tls_fingerprint_sha256`, surfacing a `tls_fingerprint_drift`
warning on a mismatch. This is the same "informational, not
authoritative" comparison described above — the warning tells the
operator that state's pin is stale (an out-of-band reissue, or a
state/vault write that never completed), not that the connection is
untrusted. It never fails the command or changes its exit code.

## Certificate rotation

### Leaf rotation (annual, or on expiry warning)

The leaf is valid for one year. `ocfp artifacts status --bloc <bloc>`
reports days remaining and warns below 30 days — check it periodically or
on a schedule rather than waiting for a handshake failure. Rotating the
leaf is a single command from anywhere with a configured
`~/.ocfp/config.pve.yml`:

```bash
ocfp artifacts provision --bloc <bloc>
```

This re-issues the leaf from the bloc CA (or generates a fresh
self-signed leaf in `self-signed` mode), restarts RustFS with the new
material, and re-syncs the fingerprint pin in state and vault. No
consumer needs to change anything: bastion, `aws` CLI, `scripts/blobstores`,
and the genesis kits all pin the **CA**, not the leaf, so a leaf swap is
transparent to every one of them. Expect a few seconds of RustFS
unavailability while the service restarts. Until this command runs, TLS
clients verifying the connection will fail their handshake once the old
leaf actually expires — plan the rotation before the expiry date rather
than reactively.

### CA rotation (every ~10 years, or on suspected compromise)

CA rotation is a coordinated, multi-step operation because every trust
distribution point (bastion, kits, cached operator copies) has to pick up
the new CA before anything that verifies against it will work again:

1. Delete the existing CA secret: `safe delete secret/ocfp/{bloc}/ca`
   (or the equivalent through your vault CLI of choice).
2. Re-provision the artifacts VM: `ocfp artifacts provision --bloc <bloc>`.
   `LoadOrGenerateBlocCA` finds nothing at the path and mints a fresh
   10-year CA plus a new leaf signed by it.
3. Re-sync vault: `ocfp vault populate --bloc <bloc>` so the new CA cert
   lands in every blobstore config path the kits read.
4. Re-run the bastion trust phase: `ocfp bastion init --bloc <bloc>` (or
   the equivalent init re-run) so `bloc_ca_trust` installs the new CA and
   drops the old one from the local trust store.
5. Redeploy any BOSH director whose `trusted_certs` still lists the old
   CA, so managed VMs (including compilation VMs) receive the new one at
   next agent bootstrap.

There is no automated CA-rotation command — this is an infrequent,
deliberate operation given the 10-year validity, and each step above has
its own blast radius worth confirming individually rather than folding
into one command.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `artifacts tls.mode=internal-ca requires an internal CA to be configured` | You are running a stale binary built before the bloc-CA wiring landed. `config.Validate` on a current build never returns this error for `internal-ca` — the internal CA is always generated on demand. | Confirm which binary you are actually running (`which -a ocfp`, `ocfp --version`); remove stray copies and rebuild with `make install-local`, the only supported local install path (it symlinks exactly one binary into `$HOME/bin/ocfp` and warns about any other `ocfp*` executables found in `$HOME/bin` or `$(GOPATH)/bin`). |
| stderr warning: `unstamped build (git commit unknown) — run make build or make install-local to embed version info` | The running binary was built with a bare `go build` (no `-ldflags` version stamping), so `ocfp --version` cannot tell you what commit you're on — the exact failure class that produces the stale-binary symptom above. | Rebuild with `make build` or `make install-local` so the binary carries its git commit and build time. |
| `artifacts ca: vault access required for bloc "<bloc>" CA material: ...` (or the equivalent from `artifacts provision`/bootstrap) | vault is unreachable or unauthenticated for a `tls.mode: internal-ca` operation. | The error names the fix in order: run `ocfp vault inception --bloc <bloc>` to start the inception vault; or export `VAULT_ADDR`/`VAULT_TOKEN` (or run `safe target <name>`) to point at a running one; or set `artifacts.tls.mode: self-signed` as a last resort. |
| `artifacts ca: bloc CA not found: secret/ocfp/<bloc>/ca for bloc "<bloc>"` | No CA has been minted yet for this bloc. | Run `ocfp vault inception --bloc <bloc>` then `ocfp artifacts provision --bloc <bloc>` to mint one, or re-run `ocfp artifacts ca` with `--generate` to mint it on the spot. |
| `artifacts CA missing from state/vault; ...` from `EndpointForLookup` (surfaces through `precompile`, `vault populate`, etc.) | `tls.mode: internal-ca` but neither state nor vault has a usable CA cert for this bloc — state may be stale. | Run `ocfp artifacts status --bloc <bloc>` and `ocfp artifacts provision --bloc <bloc>` to recover or re-mint the CA and refresh state. |
| `aws s3 ls` (or any AWS CLI v2 command) fails TLS verification even though `curl` against the same endpoint succeeds using the OS trust store | AWS CLI v2 ships its own CA bundle and ignores the OS trust store — installing the bloc CA into the system trust store alone is not enough for it. | `export AWS_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt` (already exported automatically in the bastion shell by `ocfp bastion init`) before invoking `aws`. |
| `blobstores validate` fails with `no CA available to verify <endpoint>` | None of the three CA resolution sources (`--ca-bundle`, bastion trust-store file, cached/fetched `secret/ocfp/{bloc}/ca:cert`) resolved. | Run `ocfp artifacts ca --bloc <bloc>` to fetch/cache the bloc CA, or re-run `ocfp bastion init` to (re-)install the bastion trust store, or pass `--insecure` as a documented last resort. |
| `blobstores validate` (or a manual `aws`/`curl` command) fails to verify against a `self-signed`-mode bloc that worked before this change | Expected for `self-signed` mode: there is no shared CA to install into the bastion trust store or fetch from vault, so the script now fails closed instead of silently skipping verification. Not a regression specific to your bloc — see "Upgrading existing blocs" above. | Pass `--insecure` (script) / `--no-verify-ssl`/`-k` (manual `aws`/`curl`) to restore the old skip-verify behavior, or migrate the bloc to `tls.mode: internal-ca` and re-run `ocfp artifacts provision` for verified TLS with no flags. |
| TLS verification passes for the SDN address but fails for `https://127.0.0.1:9000` on the artifacts VM itself | The leaf certificate predates the loopback-SAN addition. | Re-provision: `ocfp artifacts provision --bloc <bloc>` issues a new leaf with `127.0.0.1`/`::1` SANs. |

See also [`docs/pve.md`](pve.md#testing-the-artifacts-blobstore) for the
end-to-end validation walkthrough (`blobstores validate`, manual `aws`/
`curl` recipes, bucket sweeps).
