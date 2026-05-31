// Package probes provides a pre-deploy probe framework for PVE BOSH director
// environments. Each probe checks one health condition and returns a Result
// carrying status, optional detail, and an operator-actionable remediation
// string. RunAll executes probes in order and stops on the first failure.
package probes

import "context"

// Sentinel tokens emitted by the remote UAA Flyway bash probe. The probe
// script writes exactly one of these tokens to stdout so the caller can
// distinguish outcomes without parsing human-readable text.
const (
	// SentinelOKFresh means schema_version table does not yet exist (fresh install).
	SentinelOKFresh = "OK_FRESH"
	// SentinelOKDBMissing means the "uaa" database has not been created yet.
	SentinelOKDBMissing = "OK_DB_MISSING"
	// SentinelSkipNoPXC means the PXC MySQL job config file is absent on database/0.
	SentinelSkipNoPXC = "SKIP_NO_PXC"
	// SentinelSkipNoMySQLBin means the percona-xtradb-cluster mysql binary was not found.
	SentinelSkipNoMySQLBin = "SKIP_NO_MYSQL_BIN"
	// SentinelFailedRows is the prefix emitted when success=0 rows are found:
	// "FAILED_ROWS=N" where N is the integer row count.
	SentinelFailedRows = "FAILED_ROWS="
	// SentinelProbeError is the prefix emitted when the remote script hit an
	// unrecognised mysql error. Treated as non-fatal (logged, not failed) per
	// lab semantics: a fresh director has no CF deployment yet.
	SentinelProbeError = "PROBE_ERROR"
)

// Result is the outcome of a single Probe.Run call.
type Result struct {
	// OK is true when the probe passed (no action needed).
	OK bool
	// Detail carries additional context — the raw sentinel token for UAA probes,
	// an error message for TCP dial probes, etc.
	Detail string
	// Remediation is a human-readable operator action block. Non-empty only
	// when OK is false. RunAll includes this text in the returned error.
	Remediation string
}

// Probe is the interface implemented by every pre-deploy health check.
type Probe interface {
	// Name returns a short human-readable label used in log and error output.
	Name() string
	// Run executes the health check. The returned Result is always populated;
	// a non-nil error indicates a transport-level failure (bosh ssh unreachable,
	// TCP dial error), not an application-level FAIL verdict.
	//
	// Callers should treat a non-nil error from Run as non-fatal when the probe
	// cannot yet reach its target (e.g., CF not yet deployed). RunAll propagates
	// non-nil errors as probe failures.
	Run(ctx context.Context) Result
}

// RunAll executes probes in the supplied order and returns the Result of the
// first probe that is not OK. When all probes pass, it returns a Result with
// OK=true. The order of probes matters: RunAll aborts on the first failure so
// that the operator sees one actionable message at a time.
func RunAll(ctx context.Context, probes ...Probe) Result {
	for _, p := range probes {
		r := p.Run(ctx)
		if !r.OK {
			return r
		}
	}
	return Result{OK: true}
}
