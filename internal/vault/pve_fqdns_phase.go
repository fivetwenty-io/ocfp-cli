package vault

import (
	"errors"
	"fmt"
	"io"
	"sort"
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

// PlanFQDNs reads each plane's live fqdns record and works out which keys the
// phase would add and, under force, which existing keys it would overwrite.
// Resolution is the one ConfigureFQDNs uses (derivedFQDNRecord), so an
// explicit blocs.<bloc>.fqdns.<plane>.<svc> value wins over the derived
// default. A key vault already holds is left alone without force.
func (p *PVEVaultProvider) PlanFQDNs(force bool) ([]FQDNPlaneChanges, error) {
	plan := make([]FQDNPlaneChanges, 0, len(fqdnPlanes))

	if p.Config.FQDNs == nil {
		return plan, nil
	}

	for _, envType := range fqdnPlanes {
		path := p.PathBuilder.GetFQDNsPath(envType)

		existing, err := p.Safe.GetAll(path)
		if err != nil && !errors.Is(err, ErrSecretNotFound) {
			// An unreadable record must not be mistaken for an empty one.
			return nil, fmt.Errorf("failed to read FQDN record at %s: %w", path, err)
		}

		change := FQDNPlaneChanges{
			EnvType:    envType,
			Path:       path,
			Adds:       map[string]string{},
			Overwrites: map[string]FQDNChange{},
		}

		for key, value := range p.derivedFQDNRecord(envType) {
			derived := valueString(value)

			current, held := existing[key]

			switch {
			case !held:
				change.Adds[key] = derived
			case force && valueString(current) != derived:
				change.Overwrites[key] = FQDNChange{Old: valueString(current), New: derived}
			}
		}

		plan = append(plan, change)
	}

	return plan, nil
}

// ConfigureMissingFQDNs writes only the FQDN keys vault does not hold, per
// plane, and with force also the keys whose value differs. Only the keys to
// write are sent, and SetMultiple merges into the record, so every other key
// survives.
func (p *PVEVaultProvider) ConfigureMissingFQDNs(force bool, reporter providers.ProgressReporter) error {
	phaseStart := time.Now()

	if reporter != nil {
		reporter.ReportPhaseStart(PhaseFQDNs, 1, 1)
	}

	plan, err := p.PlanFQDNs(force)
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

	plan, err := pveProvider.PlanFQDNs(opts.Force)
	if err != nil {
		return err
	}

	err = pveProvider.ConfigureMissingFQDNs(opts.Force, opts.ProgressReporter)
	if err != nil {
		return fmt.Errorf("fqdns configuration failed: %w", err)
	}

	writeFQDNsPlan(w, m.blocName, "", plan, opts.Force, false)

	return nil
}

// populateFQDNsDryRun prints what the fqdns phase would write and writes
// nothing. The provider handed in was built over a recording safe.
func (m *Manager) populateFQDNsDryRun(provider providers.VaultProvider, force bool, target string, w io.Writer) error {
	pveProvider, ok := provider.(*PVEVaultProvider)
	if !ok {
		return fmt.Errorf("%w: got %q", ErrFQDNsRequiresPVE, m.config.Provider)
	}

	plan, err := pveProvider.PlanFQDNs(force)
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
