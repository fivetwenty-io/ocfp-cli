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

// ReservedIPObsolete is one key a reserved-ips record still holds that the
// running build's derivation no longer produces.
type ReservedIPObsolete struct {
	Path     string
	Key      string
	Existing string
}

// ReservedIPReport aggregates everything the guard withheld during one run.
type ReservedIPReport struct {
	Drifts    []ReservedIPDrift
	Schemes   []ReservedIPScheme
	Obsoletes []ReservedIPObsolete
}

// HasDrift reports whether any derived address disagreed with vault.
func (r ReservedIPReport) HasDrift() bool {
	return len(r.Drifts) > 0
}

// HasObsolete reports whether any record holds a key the derivation no
// longer produces.
func (r ReservedIPReport) HasObsolete() bool {
	return len(r.Obsoletes) > 0
}

// CompleteRecordWriter is implemented by writers that can be told a map is
// the COMPLETE derived contents of a reserved-ips record rather than a
// subset of it. Only a complete write can distinguish a key the derivation
// retired from a key this particular call simply did not carry.
type CompleteRecordWriter interface {
	SetCompleteRecord(path string, data map[string]interface{}) error
}

// setCompleteRecord writes data as the complete contents of path when safe
// supports it, and falls back to an ordinary SetMultiple otherwise — the
// fallback keeps undecorated safes (and the dry-run recorder) working, at
// the cost of not detecting obsolete keys, which is the pre-existing
// behavior rather than a regression.
func setCompleteRecord(safe SafeInterface, path string, data map[string]interface{}) error {
	if writer, ok := safe.(CompleteRecordWriter); ok {
		return writer.SetCompleteRecord(path, data)
	}

	return safe.SetMultiple(path, data) //nolint:wrapcheck // transparent pass-through
}

// reservedIPGuard decorates a SafeInterface, applying the drift policy to
// reserved-ips writes and passing every other path through untouched.
type reservedIPGuard struct {
	SafeInterface

	// force applies the derivation over the recorded addresses instead of
	// withholding it. Reallocation is a VM-recreating operation, so this is
	// only ever set by an explicit operator opt-in.
	force bool

	// scheme is the generation stamped onto a record this guard writes or
	// confirms. It comes from the netlayout.Layout resolved for the config
	// under guard, so a compact-strategy bloc is stamped "3-compact" rather
	// than the wide default.
	scheme string

	report ReservedIPReport
	logger *zap.SugaredLogger
}

// newReservedIPGuard wraps under with the reserved-IP drift policy, stamping
// records with the wide/default scheme. Callers that know which
// netlayout.Layout a config resolved to should use
// newReservedIPGuardWithScheme instead, so a compact-strategy bloc is
// stamped with its own scheme rather than wide's.
func newReservedIPGuard(under SafeInterface, force bool, log *zap.SugaredLogger) *reservedIPGuard {
	return newReservedIPGuardWithScheme(under, force, reservedIPSchemeVersion, log)
}

// newReservedIPGuardWithScheme wraps under with the reserved-IP drift
// policy, stamping records with scheme (see netlayout.Layout.SchemeVersion)
// instead of the wide default.
func newReservedIPGuardWithScheme(under SafeInterface, force bool, scheme string, log *zap.SugaredLogger) *reservedIPGuard {
	return &reservedIPGuard{
		SafeInterface: under,
		force:         force,
		scheme:        scheme,
		report:        ReservedIPReport{Drifts: nil, Schemes: nil, Obsoletes: nil},
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

	return g.guardedWrite(path, map[string]interface{}{key: value}, false)
}

func (g *reservedIPGuard) SetMultiple(path string, data map[string]interface{}) error {
	if !isReservedIPPath(path) {
		return g.SafeInterface.SetMultiple(path, data) //nolint:wrapcheck // transparent decorator
	}

	return g.guardedWrite(path, data, false)
}

// SetCompleteRecord writes data as the complete derived contents of path.
// Only this entry point can conclude that a key vault holds but data omits
// was retired from the offset table rather than merely absent from one
// partial write, so it is the only one that reports obsolete keys.
func (g *reservedIPGuard) SetCompleteRecord(path string, data map[string]interface{}) error {
	if !isReservedIPPath(path) {
		return g.SafeInterface.SetMultiple(path, data) //nolint:wrapcheck // transparent decorator
	}

	return g.guardedWrite(path, data, true)
}

// guardedWrite splits a derived reserved-ips write into the keys vault does
// not hold (forwarded) and the keys whose value it would change (recorded,
// and forwarded only under force). When complete is set, keys vault holds
// that the derivation no longer produces are reported too, and purged under
// force.
func (g *reservedIPGuard) guardedWrite(path string, data map[string]interface{}, complete bool) error {
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

	if complete && isReservedIPRecordPath(path) {
		if err := g.purgeObsolete(path, existing, data); err != nil {
			return err
		}
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

// purgeObsolete records every key the record still holds that the complete
// derivation in data does not produce, and removes it under force.
//
// These are invisible to the drift check, which can only compare keys the
// derivation emits. They are not inert: Genesis' cloud-config IPAM unions
// EVERY reserved*/available* pair it finds in a record, so a pair left
// behind by a retired band scheme silently reserves whatever range it
// spans — up to the entire subnet, which surfaces much later as the
// director failing to allocate a compilation network.
//
// The scheme stamp is the guard's own bookkeeping, not a derived address,
// so it is never a candidate.
func (g *reservedIPGuard) purgeObsolete(path string, existing, derived map[string]interface{}) error {
	obsolete := make([]string, 0, len(existing))

	for key := range existing {
		if key == reservedIPSchemeKey {
			continue
		}

		if _, produced := derived[key]; !produced {
			obsolete = append(obsolete, key)
		}
	}

	// Sorted so the report and the delete order read the same way twice.
	sort.Strings(obsolete)

	for _, key := range obsolete {
		g.report.Obsoletes = append(g.report.Obsoletes, ReservedIPObsolete{
			Path:     path,
			Key:      key,
			Existing: valueString(existing[key]),
		})

		if !g.force {
			continue
		}

		//nolint:staticcheck // explicit delegation, matching the writers above
		if err := g.SafeInterface.Delete(path, key); err != nil {
			return fmt.Errorf("failed to remove obsolete reserved-IP key %s:%s: %w", path, key, err)
		}

		delete(existing, key)
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
		if recorded != g.scheme {
			writes[reservedIPSchemeKey] = g.scheme
		}

		return
	}

	// A record that predates stamping and disagrees with the current table
	// was provisioned under an earlier scheme; so was one carrying an older
	// stamp. Both need a migration decision, not a rewrite.
	if len(existing) > 0 && recorded != g.scheme {
		g.report.Schemes = append(g.report.Schemes, ReservedIPScheme{
			Path:     path,
			Existing: recorded,
			Current:  g.scheme,
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
	if len(report.Drifts) == 0 && len(report.Schemes) == 0 && len(report.Obsoletes) == 0 {
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

	if len(report.Obsoletes) > 0 {
		_, _ = fmt.Fprintf(w,
			"\nobsolete reserved-IP keys: %d key(s) recorded in vault are no longer derived by this build.\n",
			len(report.Obsoletes))
		_, _ = fmt.Fprintln(w,
			"Genesis unions every reserved/available pair in a record, so a retired band"+
				" pair can reserve a range nothing is using — run `ocfp vault reserved-ips migrate` to remove them.")

		for _, obs := range report.Obsoletes {
			_, _ = fmt.Fprintf(w, "  %s:%s  vault=%s  (not derived)\n", obs.Path, obs.Key, obs.Existing)
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

	// Removing an obsolete key moves nothing, so the recreate warning is
	// only warranted when an address would actually change.
	if len(report.Drifts) == 0 && len(report.Schemes) == 0 {
		return
	}

	_, _ = fmt.Fprintln(w,
		"\nMoving these addresses recreates the VMs that hold them. Review with"+
			" `ocfp vault reserved-ips status`, then apply deliberately with"+
			" `ocfp vault reserved-ips migrate` (or `ocfp vault populate --force-reallocate`).")
}
