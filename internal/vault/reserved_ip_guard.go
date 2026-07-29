package vault

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"go.uber.org/zap"
)

// The reserved-IP tables in this package derive an address for every role
// from a compile-time offset map. That derivation is authoritative exactly
// once — when a bloc is provisioned. From then on vault records where those
// services physically live, and re-deriving is a proposal, not a fact.
//
// Populate used to write the derivation unconditionally. Editing the offset
// table therefore silently moved live addresses on every already-provisioned
// bloc the next time populate ran (2026-07-28: 41 and 40 live reserved IPs
// rewritten on two blocs, including director_ip). The guard below closes
// that: it forwards writes for keys vault does not yet hold, drops writes
// that would change a key vault already holds, and reports the divergence.
//
// It is a SafeInterface decorator rather than a change to each provider, so
// PVE, AWS, and STACKIT are all covered by one seam — including the write
// sites (fallback subnets, per-role sub-paths) that are easy to miss.
const (
	// reservedIPPathSegment identifies a reserved-ips record. Both the
	// keyed form (.../reserved-ips:bosh_ip) and the per-role sub-path form
	// (.../reserved-ips/bosh:ip) are guarded.
	reservedIPPathSegment = "reserved-ips"

	// reservedIPSchemeKey records which generation of the offset table a
	// path's addresses were derived from.
	reservedIPSchemeKey = "scheme_version"

	// reservedIPSchemeVersion is the current generation. "1" was the flat
	// pre-2026-07-22 layout where mgmt and ocf shared one offset window;
	// "2" is the per-tier map (see pve_reserved_ips.go). Bump this whenever
	// an edit to an offset table moves an address that blocs already
	// deployed at — not for purely additive new roles.
	reservedIPSchemeVersion = "2"
)

// ReservedIPDrift is one key whose derived address disagrees with the
// address vault already records.
type ReservedIPDrift struct {
	Path     string
	Key      string
	Existing string
	Derived  string
}

// ReservedIPScheme is one reserved-ips record whose stamped scheme version
// does not match the running binary's. Existing is empty for records
// written before stamping existed.
type ReservedIPScheme struct {
	Path     string
	Existing string
	Current  string
}

// ReservedIPReport aggregates everything the guard withheld during one run.
type ReservedIPReport struct {
	Drifts  []ReservedIPDrift
	Schemes []ReservedIPScheme
}

// HasDrift reports whether any derived address disagreed with vault.
func (r ReservedIPReport) HasDrift() bool {
	return len(r.Drifts) > 0
}

// reservedIPGuard decorates a SafeInterface, applying the drift policy to
// reserved-ips writes and passing every other path through untouched.
type reservedIPGuard struct {
	SafeInterface

	// force applies the derivation over the recorded addresses instead of
	// withholding it. Reallocation is a VM-recreating operation, so this is
	// only ever set by an explicit operator opt-in.
	force  bool
	report ReservedIPReport
	logger *zap.SugaredLogger
}

// newReservedIPGuard wraps under with the reserved-IP drift policy.
func newReservedIPGuard(under SafeInterface, force bool, log *zap.SugaredLogger) *reservedIPGuard {
	return &reservedIPGuard{
		SafeInterface: under,
		force:         force,
		report:        ReservedIPReport{Drifts: nil, Schemes: nil},
		logger:        log,
	}
}

// Report returns what the guard observed, in write order.
func (g *reservedIPGuard) Report() ReservedIPReport {
	return g.report
}

func (g *reservedIPGuard) Set(path, key string, value interface{}) error {
	if !isReservedIPPath(path) {
		return g.SafeInterface.Set(path, key, value) //nolint:wrapcheck // transparent decorator
	}

	return g.guardedWrite(path, map[string]interface{}{key: value})
}

func (g *reservedIPGuard) SetMultiple(path string, data map[string]interface{}) error {
	if !isReservedIPPath(path) {
		return g.SafeInterface.SetMultiple(path, data) //nolint:wrapcheck // transparent decorator
	}

	return g.guardedWrite(path, data)
}

// guardedWrite splits a derived reserved-ips write into the keys vault does
// not hold (forwarded) and the keys whose value it would change (recorded,
// and forwarded only under force).
func (g *reservedIPGuard) guardedWrite(path string, data map[string]interface{}) error {
	existing, err := g.readExisting(path)
	if err != nil {
		return err
	}

	writes := make(map[string]interface{}, len(data)+1)
	drifted := false

	// Sorted so the drift report reads the same way twice.
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	for _, key := range keys {
		current, held := existing[key]

		switch {
		case !held:
			writes[key] = data[key]
		case valueString(current) == valueString(data[key]):
			// Already at the derived address; nothing to do.
		default:
			drifted = true

			g.report.Drifts = append(g.report.Drifts, ReservedIPDrift{
				Path:     path,
				Key:      key,
				Existing: valueString(current),
				Derived:  valueString(data[key]),
			})

			if g.force {
				writes[key] = data[key]
			}
		}
	}

	if isReservedIPRecordPath(path) {
		g.stampScheme(path, existing, drifted, writes)
	}

	if len(writes) == 0 {
		return nil
	}

	err = g.SafeInterface.SetMultiple(path, writes)
	if err != nil {
		return fmt.Errorf("failed to write reserved IPs to %s: %w", path, err)
	}

	return nil
}

// readExisting returns the record currently at path. A genuinely empty path
// yields an empty map; any other read failure is returned, because treating
// an unreachable vault as an empty one is exactly how a live address gets
// overwritten.
func (g *reservedIPGuard) readExisting(path string) (map[string]interface{}, error) {
	//nolint:staticcheck // explicit delegation, matching the overridden writers above
	existing, err := g.SafeInterface.GetAll(path)
	if err != nil {
		if errors.Is(err, ErrSecretNotFound) {
			return map[string]interface{}{}, nil
		}

		return nil, fmt.Errorf("failed to read existing reserved IPs at %s: %w", path, err)
	}

	if existing == nil {
		return map[string]interface{}{}, nil
	}

	return existing, nil
}

// stampScheme records which generation of the offset table a record agrees
// with. The stamp is only applied when the record actually matches the
// running binary's derivation — a drifting record stays unstamped (or keeps
// its older stamp) and is reported instead, so the stamp never claims a
// conformance that was never applied.
func (g *reservedIPGuard) stampScheme(
	path string, existing map[string]interface{}, drifted bool, writes map[string]interface{},
) {
	recorded := valueString(existing[reservedIPSchemeKey])

	if !drifted || g.force {
		if recorded != reservedIPSchemeVersion {
			writes[reservedIPSchemeKey] = reservedIPSchemeVersion
		}

		return
	}

	// A record that predates stamping and disagrees with the current table
	// was provisioned under an earlier scheme; so was one carrying an older
	// stamp. Both need a migration decision, not a rewrite.
	if len(existing) > 0 && recorded != reservedIPSchemeVersion {
		g.report.Schemes = append(g.report.Schemes, ReservedIPScheme{
			Path:     path,
			Existing: recorded,
			Current:  reservedIPSchemeVersion,
		})
	}
}

// isReservedIPPath reports whether path addresses a reserved-ips record or
// one of its per-role sub-paths.
func isReservedIPPath(path string) bool {
	return slices.Contains(strings.Split(strings.Trim(path, "/"), "/"), reservedIPPathSegment)
}

// isReservedIPRecordPath reports whether path is the reserved-ips record
// itself rather than a per-role sub-path. Only the record carries a scheme
// stamp; a sub-path holds a single address and nothing else.
func isReservedIPRecordPath(path string) bool {
	segments := strings.Split(strings.Trim(path, "/"), "/")

	return len(segments) > 0 && segments[len(segments)-1] == reservedIPPathSegment
}

// valueString renders a vault value for comparison and display. Reserved-IP
// records hold addresses and range bounds, never credentials, so printing
// them is safe.
func valueString(value interface{}) string {
	if value == nil {
		return ""
	}

	if s, ok := value.(string); ok {
		return s
	}

	return fmt.Sprintf("%v", value)
}

// WriteReservedIPReport renders reserved-IP changes to w. Nothing
// is printed when there is nothing to report, so a clean populate stays
// quiet. Write errors are ignored: this goes to a console writer and a
// failed print must not fail the run.
func WriteReservedIPReport(w io.Writer, report ReservedIPReport) {
	if len(report.Drifts) == 0 && len(report.Schemes) == 0 {
		return
	}

	if len(report.Drifts) > 0 {
		_, _ = fmt.Fprintf(w,
			"\nreserved-IP drift: %d address(es) already recorded in vault differ from this build's table.\n",
			len(report.Drifts))
		_, _ = fmt.Fprintln(w, "The recorded addresses were kept. Nothing was moved.")

		for _, drift := range report.Drifts {
			_, _ = fmt.Fprintf(w, "  %s:%s  vault=%s  derived=%s\n",
				drift.Path, drift.Key, drift.Existing, drift.Derived)
		}
	}

	for _, scheme := range report.Schemes {
		recorded := scheme.Existing
		if recorded == "" {
			recorded = "unstamped"
		}

		_, _ = fmt.Fprintf(w, "  %s was provisioned under reserved-IP scheme %s; this build derives scheme %s\n",
			scheme.Path, recorded, scheme.Current)
	}

	_, _ = fmt.Fprintln(w,
		"\nMoving these addresses recreates the VMs that hold them. Review with"+
			" `ocfp vault reserved-ips status`, then apply deliberately with"+
			" `ocfp vault reserved-ips migrate` (or `ocfp vault populate --force-reallocate`).")
}
