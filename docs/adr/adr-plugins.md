# ADR-0001 — Plugin Architecture for `ocfp` CLI (HashiCorp go-plugin, gRPC)

**Date:** 2025-08-09
**Status:** Accepted
**Authors/Deciders:** OCFP Core
**Context:** ocfp (successor to the Genesis CLI) needs a robust, portable plugin system for “kits” that model BOSH deployments, environments, and feature-driven templating. Kits must call back into the host for utilities (e.g., *Exodus data lookup*, YAML merge/interpolation, secrets access, metadata discovery) while remaining isolated and versionable.

## Decision

We will implement the `ocfp` plugin system using **HashiCorp `go-plugin` in gRPC mode**, with the **gRPC Broker** for host callbacks. Each kit is a separate executable launched by `ocfp`. The host and kit communicate via a versioned, protobuf-defined API. The host exposes a `HostUtility` service the kit can call to perform privileged/standardized operations (e.g., `GetExodusValue`).

## Rationale (Decision Drivers)

* **Typed, stable contracts:** Protobuf definitions make the API explicit and evolvable (backward-compatible fields, deprecation, version gates).
* **Process isolation & safety:** Kit crashes do not crash `ocfp`; resource limits and timeouts remain under host control.
* **Bidirectional RPC:** The gRPC Broker enables kits to invoke host utilities without inventing ad‑hoc IPC.
* **Cross-language option:** Kits may be authored in Go or any language with gRPC support (preferred: Go).
* **Operational maturity:** Aligns with Terraform/Vault/Nomad style plugin ergonomics we understand well.

## Scope

* **In scope:** Kit runtime contracts; discovery; lifecycle; version negotiation; host-utility surface; packaging; security and observability basics; migration guidance.
* **Out of scope (future ADRs):** Remote plugin execution over the network; full WASM sandbox; signed plugin distribution and trust policy.

## Architecture Overview

```
+------------------+          gRPC           +------------------+
|     ocfp host    | <-------------------->  |     Kit binary   |
| (go-plugin host) |  (go-plugin, Broker)    | (go-plugin impl) |
+------------------+                         +------------------+
      ^      ^                                        ^
      |      |                                        |
  Host-      |                                Kit-side API
 Utilities   |                              (Render/Validate/etc)
 (gRPC)      |
        Discovery & Launch (exec)
```

### Plugin Process Model

* `ocfp` discovers candidate plugins and launches selected kit processes.
* A **handshake** validates protocol and API versions; capabilities are exchanged.
* The kit exposes the **Kit service**; the host exposes **HostUtility** via the Broker; both sides hold references supplied at initialization.

## Public Interfaces (initial draft)

### Protobuf package: `ocfp.kit.v1`

```proto
syntax = "proto3";
package ocfp.kit.v1;

message InitRequest {
  string host_broker_id = 1;         // for host callbacks
  string ocfp_version = 2;
  string api_version = 3;            // e.g., "v1"
  map<string,string> env = 4;        // sanitized env vars, paths
}

message InitResponse {
  string kit_name = 1;
  string kit_version = 2;            // semver
  repeated string capabilities = 3;  // e.g., ["render","validate","plan"]
}

message EnvContext {
  bytes env_yaml = 1;                 // raw YAML for environment
  repeated bytes ops_files = 2;       // optional ops overlays
  map<string,string> params = 3;      // user-supplied params
  repeated string features = 4;       // enabled feature flags
  string workspace_dir = 5;           // scratch working directory
  string env_name = 6;                // logical env identifier
  string kit_name = 7;                // kit identifier
}

message Diagnostics { repeated Diagnostic items = 1; }
message Diagnostic {
  enum Level { INFO = 0; WARN = 1; ERROR = 2; }
  Level level = 1;
  string code = 2;
  string message = 3;
  string path = 4; // YAML or logical path
}

message RenderResult {
  // Manifest artifacts: paths are relative to workspace_dir
  repeated FileArtifact manifests = 1;  // e.g., BOSH manifests
  repeated FileArtifact vars_files = 2;
  bytes render_index_json = 3;          // machine-readable index
}

message FileArtifact { string path = 1; bytes contents = 2; }

service Kit {
  rpc Init(InitRequest) returns (InitResponse);
  rpc Features(EnvContext) returns (FeatureSet);
  rpc Validate(EnvContext) returns (Diagnostics);
  rpc Render(EnvContext) returns (RenderResult);
  rpc Plan(EnvContext) returns (PlanResult);
}

message FeatureSet { repeated string available = 1; }
message PlanResult { bytes plan_json = 1; }
```

### Host Utility callbacks: `ocfp.host.v1`

```proto
syntax = "proto3";
package ocfp.host.v1;

message ExodusKey {
  string env = 1;   // env name
  string kit = 2;   // kit name
  string path = 3;  // path within exodus tree
  string key  = 4;  // final key
}

message ExodusPath { string env = 1; string kit = 2; string path = 3; }
message KV { string key = 1; bytes value = 2; }
message Value { bytes data = 1; string content_type = 2; }

message MergeRequest { repeated bytes yamls = 1; }
message MergeResult { bytes merged_yaml = 1; }

message SecretRef { string mount = 1; string name = 2; }
message Secret { map<string,bytes> fields = 1; }

message InterpolateRequest { bytes template = 1; map<string,bytes> vars = 2; }
message Interpolated { bytes result = 1; }

message LogEntry { string level = 1; string message = 2; map<string,string> fields = 3; }
message Empty {}

service HostUtility {
  rpc GetExodusValue(ExodusKey) returns (Value);
  rpc GetExodusTree(ExodusPath) returns (stream KV);
  rpc MergeYAML(MergeRequest) returns (MergeResult);
  rpc VaultRead(SecretRef) returns (Secret);
  rpc Interpolate(InterpolateRequest) returns (Interpolated);
  rpc Log(LogEntry) returns (Empty);
}
```

### Handshake & Capability Exchange

* **Protocol:** go-plugin (gRPC) handshake; both sides report `protocolVersion`, `apiVersion` (`ocfp.kit.v1`), and semantic versions.
* **Capabilities:** The kit lists supported hooks; the host lists available utilities. Unknown capability → negotiated fallback or failure with explicit error.

## Discovery & Packaging

* **Discovery locations:**

  * Configured paths in `~/.config/ocfp/config.yml` (kit search paths).
  * Default search at `${OCFP_KIT_PATH}` and `${PATH}` for executables.
* **Package layout (recommendation):**
  * `ocfp-kit-<name>` (binary)
  * `kit.yaml` (manifest: name, version, capabilities, minimum ocfp/api)
  * Optional assets: templates, default vars, docs
* **Distribution:** local filesystem or Git (tagged releases). Checksums required; signature support tracked as a follow-up.

## Versioning & Compatibility

* **API versioning:** `ocfp.kit.v1` is the initial stable API. Backward-compatible changes only (additive fields, reserved tags). Breaking changes → `v2`.
* **Semantic versions:** `ocfp` and kits use SemVer. The host refuses incompatible major versions.
* **Negotiation:** During `Init`, both sides exchange `(ocfpVersion, apiVersion, kitVersion)` and a list of capabilities.

## Security Considerations

* **Process isolation:** Kits run out-of-process; host enforces timeouts, max message sizes, and optional sandboxing of working directories.
* **I/O boundaries:** Kits write only within a provided `workspace_dir`. Host mediates secrets and network calls via `HostUtility`.
* **Verification:** Checksum verification on fetch/install; optional allowlist of kit publishers.
* **Least privilege:** `HostUtility` surface area is minimal; each call is audited.

## Observability & Diagnostics

* **Structured logs:** Kits send log entries via `HostUtility.Log`; host prefixes with kit identity and correlates with request IDs.
* **Trace IDs:** `ocfp` injects a `request_id`/`trace_id` into `EnvContext`; flows must include it in logs and errors.
* **Metrics:** Call counts, durations, and error rates per RPC. Expose with a `--metrics` flag (Prometheus endpoint when running long tasks) or aggregate in-process for CLI output.

## Error Handling

* **Typed errors:** Protobuf error codes with optional remediation hints. Validation errors surface as `Diagnostics` (not transport errors).
* **Timeouts & retries:** Host sets RPC deadlines; idempotent calls may be retried.
* **Crash recovery:** Host reports clear messages when a kit exits unexpectedly; includes last logs and handshake info.

## Testing Strategy

* **Contract tests:** Golden tests for protobuf messages and wire-compat.
* **Fake host utilities:** Kits test against a mock `HostUtility` server.
* **End-to-end harness:** Launch kits in CI with representative envs; assert artifacts and diagnostics.

## Migration (Genesis → ocfp)

* Provide an adapter library for kit authors with helpers: `Context`, `Diagnostics`, `RenderResult` builders.
* Offer a reference kit showcasing `Validate/Render/Plan` and calls to `GetExodusValue`, `MergeYAML`, `VaultRead`.
* Supply converters for common Genesis structures (env YAML, features, params) and for Spruce-like merges.

## Risks & Mitigations

* **Version skew:** Mitigate with strict handshake and capability negotiation.
* **Performance overhead:** Keep utility calls coarse-grained (e.g., fetch an exodus subtree rather than many single-key calls); support streaming APIs.
* **Distribution sprawl:** Standardize `kit.yaml` and provide `ocfp kit install|update|verify` commands.

## Alternatives Considered

* **Exec plugins (kubectl/git-style):** Simple JSON-over-stdio, low barrier; lacks typed contracts and robust bidirectional RPC.
* **Go `buildmode=plugin`:** In-process, but poor portability and toolchain coupling.
* **WASM (`wazero`):** Strong sandboxing and portability; higher initial complexity; better as a future option once the host ABI stabilizes.
* **Embedded scripting (Starlark):** Great for small hook logic; not suitable as the primary kit runtime.

## Decision Outcome

Adopting go-plugin (gRPC + Broker) gives us isolation, typed interfaces, and clean host-utility callbacks, enabling reliable Exodus data queries and other shared services without sacrificing portability.

## Acceptance Criteria

* A reference kit can:

  1. `Validate` an environment and emit structured diagnostics.
  2. `Render` manifests and a machine-readable index.
  3. Call `GetExodusValue` and `GetExodusTree` via host callbacks.
* Version handshake rejects incompatible kits with a clear error.
* Crash of a kit process does not crash `ocfp`.
* End-to-end tests verify the above on macOS/Linux/Windows.

## Rollout Plan

1. Implement protobufs and host/kit scaffolding; ship a reference kit.
2. Add discovery & `kit.yaml` support; build `ocfp kit install|list|verify`.
3. Migrate one production kit; capture feedback.
4. Stabilize `v1` API; document authoring guide and templates.
5. Evaluate signing, policy, and optional WASM track in follow-up ADRs.

## Open Questions

* Do we require offline operation for all host utilities, or allow kits to perform direct network calls with policy?
* What is the minimum supported Go/toolchain for official kits?
* Should we add a lightweight exec-plugin adapter for simple shell-based kits as a compatibility layer?

