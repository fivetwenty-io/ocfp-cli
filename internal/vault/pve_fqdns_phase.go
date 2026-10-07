package vault

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/providers"
)

// fqdnPlanes are the planes the fqdns phase covers, in report order.
var fqdnPlanes = []string{MgmtEnvType, OCFEnvType}

// FQDNChange is one existing key whose recorded value differs from the
// resolved one. It is only populated when the phase runs with force.
type FQDNChange struct {
	Old string
	New string
}

// FQDNPlaneChanges is what the fqdns phase would write to one plane's record.
type FQDNPlaneChanges struct {
	EnvType    string
	Path       string
	Adds       map[string]string
	Overwrites map[string]FQDNChange
}

// Empty reports whether the plane needs no write.
func (c FQDNPlaneChanges) Empty() bool {
	return len(c.Adds) == 0 && len(c.Overwrites) == 0
}

// fqdnDescriptiveKeys are written to every plane record but describe the
// record rather than a service, so --key cannot select them.
var fqdnDescriptiveKeys = []string{"env_type", "base"}

// FQDNFilter narrows the fqdns phase. An empty Planes keeps every plane and an
// empty Keys keeps every key, so the zero value is the unfiltered phase.
type FQDNFilter struct {
	// Planes keeps only the named planes (mgmt, ocf).
	Planes []string

	// Keys keeps only the named service keys, within the selected planes.
	Keys []string
}

// Empty reports whether the filter selects everything.
func (f FQDNFilter) Empty() bool {
	return len(f.Planes) == 0 && len(f.Keys) == 0
}

// ValidateFQDNFlagValues rejects a --plane or --key flag that was given but
// holds no usable value. pflag turns an explicit empty string into an empty
// slice, which the filter would read as no filter at all, so the command calls
// this with whether the flag was set. The values need at least one entry, and
// no entry may be empty or all whitespace, which also catches a stray comma.
func ValidateFQDNFlagValues(flag string, given bool, values []string) error {
	if !given {
		return nil
	}

	if len(values) == 0 {
		return fmt.Errorf("%w: --%s was given without a value", ErrFQDNFilterEmptyValue, flag)
	}

	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: --%s has an empty item in %q", ErrFQDNFilterEmptyValue, flag, strings.Join(values, ","))
		}
	}

	return nil
}

// ValidateScope rejects a filter handed to a phase other than fqdns, and a
// plane name the phase does not cover. It needs no config and reads nothing,
// so the command can run it before it touches vault.
func (f FQDNFilter) ValidateScope(phase string) error {
	if phase != PhaseFQDNs && !f.Empty() {
		return fmt.Errorf("%w: the %q phase was given %s", ErrFQDNFilterWrongPhase, phase, f.describe())
	}

	_, err := f.selectedPlanes()

	return err
}

// describe names the filter flags that were set, for error messages.
func (f FQDNFilter) describe() string {
	var set []string

	if len(f.Planes) > 0 {
		set = append(set, "--plane")
	}

	if len(f.Keys) > 0 {
		set = append(set, "--key")
	}

	return strings.Join(set, " and ")
}

// selectedPlanes returns the planes the filter keeps, in report order, or
// ErrUnknownFQDNPlane for a name the phase does not cover.
func (f FQDNFilter) selectedPlanes() ([]string, error) {
	for _, plane := range f.Planes {
		if !slices.Contains(fqdnPlanes, plane) {
			return nil, fmt.Errorf("%w %q; valid planes are %s", ErrUnknownFQDNPlane, plane, strings.Join(fqdnPlanes, ", "))
		}
	}

	if len(f.Planes) == 0 {
		return fqdnPlanes, nil
	}

	selected := make([]string, 0, len(fqdnPlanes))

	for _, plane := range fqdnPlanes {
		if slices.Contains(f.Planes, plane) {
			selected = append(selected, plane)
		}
	}

	return selected, nil
}

// keepsKey reports whether key passes the key filter.
func (f FQDNFilter) keepsKey(key string) bool {
	return len(f.Keys) == 0 || slices.Contains(f.Keys, key)
}

// selectableFQDNKeys returns the service keys of a derived record, sorted,
// without the descriptive keys.
func selectableFQDNKeys(record map[string]any) []string {
	keys := make([]string, 0, len(record))

	for key := range record {
		if !slices.Contains(fqdnDescriptiveKeys, key) {
			keys = append(keys, key)
		}
	}

	sort.Strings(keys)

	return keys
}

// validateKeys checks every requested key against the derived records of the
// selected planes. A key must exist in at least one of them.
func (f FQDNFilter) validateKeys(selected []string, records map[string]map[string]any) error {
	if len(f.Keys) == 0 {
		return nil
	}

	valid := map[string]bool{}

	for _, plane := range selected {
		for _, key := range selectableFQDNKeys(records[plane]) {
			valid[key] = true
		}
	}

	for _, key := range f.Keys {
		if !valid[key] {
			names := make([]string, 0, len(valid))
			for name := range valid {
				names = append(names, name)
			}

			sort.Strings(names)

			return fmt.Errorf("%w %q for plane(s) %s; valid keys are %s",
				ErrUnknownFQDNKey, key, strings.Join(selected, ", "), strings.Join(names, ", "))
		}
	}

	return nil
}

// PlanFQDNs reads each selected plane's live fqdns record and works out which
// keys the phase would add and, under force, which existing keys it would
// overwrite. Resolution is the one ConfigureFQDNs uses (derivedFQDNRecord), so
// an explicit blocs.<bloc>.fqdns.<plane>.<svc> value wins over the derived
// default. A key vault already holds is left alone without force.
//
// The filter narrows the plan itself, so the dry run, the write, and the
// printed summary all see the same keys. The filter is checked before any
// read of vault, and a bad plane or key fails the whole plan.
func (p *PVEVaultProvider) PlanFQDNs(force bool, filter FQDNFilter) ([]FQDNPlaneChanges, error) {
	selected, err := filter.selectedPlanes()
	if err != nil {
		return nil, err
	}

	plan := make([]FQDNPlaneChanges, 0, len(selected))

	if p.Config.FQDNs == nil {
		if len(filter.Keys) > 0 {
			return nil, ErrFQDNKeysUncheckable
		}

		return plan, nil
	}

	records := make(map[string]map[string]any, len(selected))
	for _, envType := range selected {
		records[envType] = p.derivedFQDNRecord(envType)
	}

	err = filter.validateKeys(selected, records)
	if err != nil {
		return nil, err
	}

	for _, envType := range selected {
		change, err := p.planFQDNPlane(envType, records[envType], force, filter)
		if err != nil {
			return nil, err
		}

		plan = append(plan, change)
	}

	return plan, nil
}

// planFQDNPlane diffs one plane's derived record against what vault holds.
func (p *PVEVaultProvider) planFQDNPlane(envType string, record map[string]any, force bool, filter FQDNFilter) (FQDNPlaneChanges, error) {
	path := p.PathBuilder.GetFQDNsPath(envType)

	existing, err := p.Safe.GetAll(path)
	if err != nil && !errors.Is(err, ErrSecretNotFound) {
		// An unreadable record must not be mistaken for an empty one.
		return FQDNPlaneChanges{}, fmt.Errorf("failed to read FQDN record at %s: %w", path, err)
	}

	change := FQDNPlaneChanges{
		EnvType:    envType,
		Path:       path,
		Adds:       map[string]string{},
		Overwrites: map[string]FQDNChange{},
	}

	for key, value := range record {
		if !filter.keepsKey(key) {
			continue
		}

		derived := valueString(value)

		current, held := existing[key]

		switch {
		case !held:
			change.Adds[key] = derived
		case force && valueString(current) != derived:
			change.Overwrites[key] = FQDNChange{Old: valueString(current), New: derived}
		}
	}

	return change, nil
}

// ConfigureMissingFQDNs writes only the FQDN keys vault does not hold, per
// plane, and with force also the keys whose value differs. Only the keys to
// write are sent, and SetMultiple merges into the record, so every other key
// survives.
func (p *PVEVaultProvider) ConfigureMissingFQDNs(force bool, reporter providers.ProgressReporter) error {
	return p.ConfigureFQDNsScoped(force, FQDNFilter{}, reporter)
}

// ConfigureFQDNsScoped is ConfigureMissingFQDNs limited to the planes and keys
// the filter selects.
func (p *PVEVaultProvider) ConfigureFQDNsScoped(force bool, filter FQDNFilter, reporter providers.ProgressReporter) error {
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(PhaseFQDNs, 1, 1)
	}

	plan, err := p.PlanFQDNs(force, filter)
	if err != nil {
		return err
	}

	for _, change := range plan {
		if change.Empty() {
			continue
		}

		data := make(map[string]interface{}, len(change.Adds)+len(change.Overwrites))
		for key, value := range change.Adds {
			data[key] = value
		}

		for key, over := range change.Overwrites {
			data[key] = over.New
		}

		err = p.Safe.SetMultiple(change.Path, data)
		if err != nil {
			return fmt.Errorf("failed to set FQDN configuration at %s: %w", change.Path, err)
		}
	}

	if reporter != nil {
		reporter.ReportPhaseComplete(PhaseFQDNs, time.Since(phaseStart))
	}

	return nil
}

// populateFQDNsPhase is the fqdns arm of populate.
func (m *Manager) populateFQDNsPhase(opts *PopulateOptions, w io.Writer) error {
	m.logger.Infow("Populating FQDNs to vault", "provider", m.config.Provider)

	pveProvider, err := m.fqdnsProvider(m.safe)
	if err != nil {
		return err
	}

	filter := opts.fqdnFilter()

	plan, err := pveProvider.PlanFQDNs(opts.Force, filter)
	if err != nil {
		return err
	}

	err = pveProvider.ConfigureFQDNsScoped(opts.Force, filter, opts.ProgressReporter)
	if err != nil {
		return fmt.Errorf("fqdns configuration failed: %w", err)
	}

	writeFQDNsPlan(w, m.blocName, "", plan, opts.Force, false)

	return nil
}

// populateFQDNsDryRun prints what the fqdns phase would write and writes
// nothing. The provider handed in was built over a recording safe.
func (m *Manager) populateFQDNsDryRun(provider providers.VaultProvider, force bool, filter FQDNFilter, target string, w io.Writer) error {
	pveProvider, ok := provider.(*PVEVaultProvider)
	if !ok {
		return fmt.Errorf("%w: got %q", ErrFQDNsRequiresPVE, m.config.Provider)
	}

	plan, err := pveProvider.PlanFQDNs(force, filter)
	if err != nil {
		return err
	}

	writeFQDNsPlan(w, m.blocName, target, plan, force, true)

	return nil
}

// fqdnsProvider builds the PVE provider over safe, or names the limitation.
func (m *Manager) fqdnsProvider(safe SafeInterface) (*PVEVaultProvider, error) {
	provider, err := m.createVaultProviderWith(safe)
	if err != nil {
		return nil, fmt.Errorf("failed to create vault provider: %w", err)
	}

	pveProvider, ok := provider.(*PVEVaultProvider)
	if !ok {
		return nil, fmt.Errorf("%w: got %q", ErrFQDNsRequiresPVE, m.config.Provider)
	}

	return pveProvider, nil
}

// writeFQDNsPlan renders the per-plane additions, and under force the
// overwrites with old and new values. FQDNs are hostnames, not secrets, so
// values are printed. An empty plan means the bloc has no fqdns configuration,
// so the phase evaluated nothing, and the output says so.
func writeFQDNsPlan(w io.Writer, bloc, target string, plan []FQDNPlaneChanges, force, dryRun bool) {
	prefix, addVerb, overVerb := "", "added", "overwritten"

	if dryRun {
		prefix, addVerb, overVerb = "[DRY RUN] ", "would add", "would overwrite"

		_, _ = fmt.Fprintf(w, "%svault populate fqdns plan — target: %s\n", prefix, target)
	}

	total := 0

	if len(plan) == 0 {
		_, _ = fmt.Fprintf(w, "%sNo fqdns are configured for bloc %s, so nothing was evaluated; add an fqdns block to the bloc config to populate them\n", prefix, bloc)
	}

	for _, change := range plan {
		if change.Empty() {
			_, _ = fmt.Fprintf(w, "%s%s (%s): nothing to %s\n", prefix, change.EnvType, change.Path, addVerbShort(dryRun))

			continue
		}

		_, _ = fmt.Fprintf(w, "%s%s (%s)\n", prefix, change.EnvType, change.Path)

		for _, key := range sortedKeys(change.Adds) {
			total++

			_, _ = fmt.Fprintf(w, "%s  %s: %s = %s\n", prefix, addVerb, key, change.Adds[key])
		}

		if !force {
			continue
		}

		overKeys := make([]string, 0, len(change.Overwrites))
		for key := range change.Overwrites {
			overKeys = append(overKeys, key)
		}

		sort.Strings(overKeys)

		for _, key := range overKeys {
			total++

			_, _ = fmt.Fprintf(w, "%s  %s: %s: %s -> %s\n", prefix, overVerb, key,
				change.Overwrites[key].Old, change.Overwrites[key].New)
		}
	}

	if dryRun {
		_, _ = fmt.Fprintf(w, "%s%d key(s); no changes made\n", prefix, total)
	}
}

func addVerbShort(dryRun bool) string {
	if dryRun {
		return "add"
	}

	return "write"
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}
