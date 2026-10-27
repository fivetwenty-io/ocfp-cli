package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/blocinv"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/gcp"
	"github.com/ocfp/ocfp-cli-go/internal/cpi/pve"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// blocLiveTimeout bounds all the provider calls one `bloc show --live` makes,
// so an unreachable provider degrades to errors in the Live section instead of
// hanging the command. Tests shorten it.
var blocLiveTimeout = 60 * time.Second

// Errors recorded in the Live section.
var (
	ErrBlocLiveNoConfig  = errors.New("no config defines this bloc, so no provider can be reached")
	ErrBlocLiveNoBastion = errors.New("no bastion id in state")

	// errBlocLiveAmbiguousVM means several of the bloc's VMs carry the exact
	// name searched for, so none can be chosen safely.
	errBlocLiveAmbiguousVM = errors.New("more than one VM is named")
)

// blocComputeReader is the read-only slice of the provider's compute manager
// that `bloc show --live` uses. Keeping it to two read calls means the live
// path cannot start, stop, or change a VM.
type blocComputeReader interface {
	GetInstance(ctx context.Context, id string) (*cpi.Instance, error)
	ListInstances(ctx context.Context, filters map[string]string) ([]*cpi.Instance, error)
}

// blocLiveSource is what the live path needs from the provider setup.
type blocLiveSource struct {
	Reader           blocComputeReader
	ArtifactsEnabled bool
	Close            func()
}

// newBlocLiveSource builds the live source for a bloc from the config file
// that defines it. Tests replace it with a fake so no provider is ever
// contacted.
var newBlocLiveSource = openBlocLiveSource

// openBlocLiveSource loads the bloc's config from the file that defines it,
// which is not always the --config file and may be a flat bloc file, and
// connects its provider. Every
// error it returns is classified, so its text is safe to show.
func openBlocLiveSource(ctx context.Context, blocName, configFile string) (blocLiveSource, error) {
	if configFile == "" {
		return blocLiveSource{}, &blocinv.ClassifiedError{Kind: blocinv.KindBlocNotInConfig, Err: ErrBlocLiveNoConfig}
	}

	cfg, err := config.LoadBlocFromFile(configFile, blocName)
	if err != nil {
		return blocLiveSource{}, classifyLiveConfigError(err, configFile)
	}

	iaas, region, err := determineProviderAndRegion(cfg)
	if err != nil {
		return blocLiveSource{}, &blocinv.ClassifiedError{Kind: blocinv.KindConfigInvalid, Path: configFile, Err: err}
	}

	provider, err := createProvider(ctx, iaas, buildProviderConfig(cfg, region))
	if err != nil {
		return blocLiveSource{}, classifyProviderError(err, blocinv.KindProviderSetup, "")
	}

	return blocLiveSource{
		Reader:           provider.ComputeManager(),
		ArtifactsEnabled: cfg.Artifacts.Enabled,
		Close:            func() { _ = provider.Cleanup(context.Background()) },
	}, nil
}

// blocLiveReport is the Live section of `bloc show --live`. Each part carries
// its own error, so one failed read never hides the other.
type blocLiveReport struct {
	Bastion   blocLiveBastion   `json:"bastion"`
	Artifacts blocLiveArtifacts `json:"artifacts"`
}

type blocLiveBastion struct {
	VMID  string `json:"vm_id"`
	Power string `json:"power"`
	Error string `json:"error"`
	// Note explains a result that is right but unexpected, such as a bastion
	// found under a different VM id than state records.
	Note string `json:"note,omitempty"`
}

type blocLiveArtifacts struct {
	VMID     string `json:"vm_id"`
	Power    string `json:"power"`
	Endpoint string `json:"endpoint"`
	TLSMode  string `json:"tls_mode"`
	Error    string `json:"error"`
}

// collectBlocLive makes the read-only provider calls. It never returns an
// error: every failure lands in the part of the report it belongs to.
func collectBlocLive(ctx context.Context, entry blocinv.Entry, summary blocinv.Summary) *blocLiveReport {
	report := &blocLiveReport{}

	if !entry.HasConfig {
		report.Bastion.Error = ErrBlocLiveNoConfig.Error()
		report.Artifacts.Error = ErrBlocLiveNoConfig.Error()

		return report
	}

	ctx, cancel := context.WithTimeout(ctx, blocLiveTimeout)
	defer cancel()

	source, err := newBlocLiveSource(ctx, entry.Name, entry.ConfigFile)
	if err != nil {
		message := blocinv.SafeMessage(err, "")
		report.Bastion.Error = message
		report.Artifacts.Error = message

		return report
	}

	if source.Close != nil {
		defer source.Close()
	}

	if summary.State != nil {
		report.Bastion.VMID = summary.State.Bastion.ID
	}

	readBastion(ctx, source.Reader, entry.Name, &report.Bastion)
	readArtifacts(ctx, source, entry, &report.Artifacts)

	return report
}

// readBastion fills in the bastion's power state from the provider. When the
// provider says the VM id in state does not exist, it looks the bastion up by
// its name, since a bastion that was moved or rebuilt gets a new id while
// state keeps the old one. Any other failure, such as a refused permission or
// a timeout, says nothing about whether the VM exists, so it is reported as it
// is and no search is made.
func readBastion(ctx context.Context, reader blocComputeReader, blocName string, bastion *blocLiveBastion) {
	if bastion.VMID == "" {
		bastion.Error = ErrBlocLiveNoBastion.Error()

		return
	}

	stateID := bastion.VMID

	inst, err := reader.GetInstance(ctx, stateID)
	if err == nil {
		bastion.Power = string(inst.State)

		return
	}

	if !isInstanceNotFound(err) {
		bastion.Error = blocinv.SafeMessage(classifyProviderError(err, blocinv.KindProviderRequest, stateID), "")

		return
	}

	found, listErr := findBastionByName(ctx, reader, blocName)

	switch {
	case errors.Is(listErr, errBlocLiveAmbiguousVM):
		bastion.Error = fmt.Sprintf("VM %s not found, and %v", stateID, listErr)
	case listErr != nil:
		bastion.Error = fmt.Sprintf("VM %s not found, and the search by name failed: %s", stateID, blocinv.SafeMessage(listErr, ""))
	case found == nil:
		bastion.Error = fmt.Sprintf("VM %s not found", stateID)
	case found.ID == stateID:
		// The list has the VM the direct read could not find, so state is
		// right and the read failed for some other reason.
		bastion.Power = string(found.State)
		bastion.Note = fmt.Sprintf("VM %s could not be read directly, so its power comes from the provider's VM list", stateID)
	default:
		bastion.VMID = found.ID
		bastion.Power = string(found.State)
		bastion.Note = fmt.Sprintf("state records VM %s, which does not exist; the bastion is VM %s, so state is stale", stateID, found.ID)
	}
}

// blocOwnerTags are the tags bootstrap puts on every VM it creates for a
// bloc.
func blocOwnerTags(blocName string) map[string]string {
	return map[string]string{"bloc": blocName, "managed-by": "ocfp"}
}

// artifactsOwnerTags are the tags bootstrap adds to a bloc's artifacts VM.
func artifactsOwnerTags(blocName string) map[string]string {
	return map[string]string{"ocfp:role": "artifacts", "ocfp:bloc": blocName}
}

// findBastionByName finds the bloc's bastion among the provider's VMs by the
// bloc's ownership tags and the exact name "<bloc>-bastion". A prefix or
// substring match could pick another bloc's VM, so only the exact name
// counts. It returns nil with no error when no VM matches, and an error when
// more than one VM does.
func findBastionByName(ctx context.Context, reader blocComputeReader, blocName string) (*cpi.Instance, error) {
	return findOwnedVM(ctx, reader, blocName+"-bastion", blocOwnerTags(blocName))
}

// findOwnedVM lists the VMs that carry tags and returns the one named vmName.
// Each provider reads a "label.<key>" filter as a tag or label to match, and
// PVE reads no other form, so the filters use that prefix. A provider can
// still return a VM the filters should have excluded, so each VM must carry
// the tags itself to count. It returns nil with no error when no VM matches,
// and errBlocLiveAmbiguousVM when VMs with different ids do.
func findOwnedVM(ctx context.Context, reader blocComputeReader, vmName string, tags map[string]string) (*cpi.Instance, error) {
	filters := make(map[string]string, len(tags))
	for key, value := range tags {
		filters["label."+key] = value
	}

	instances, err := reader.ListInstances(ctx, filters)
	if err != nil {
		return nil, classifyProviderError(err, blocinv.KindProviderRequest, "")
	}

	var match *cpi.Instance

	for _, inst := range instances {
		if inst == nil || inst.Name != vmName || !instanceCarriesTags(inst, tags) {
			continue
		}

		if match != nil && match.ID != inst.ID {
			return nil, fmt.Errorf("%w %s", errBlocLiveAmbiguousVM, vmName)
		}

		match = inst
	}

	return match, nil
}

// instanceCarriesTags reports whether inst carries every key=value in tags.
func instanceCarriesTags(inst *cpi.Instance, tags map[string]string) bool {
	for key, value := range tags {
		if !instanceHasTag(inst, key, value) {
			return false
		}
	}

	return true
}

// instanceHasTag reports whether inst carries key=value in any form a
// provider returns tags in. AWS, Azure, and STACKIT keep the key and value as
// written. PVE turns the pair into one tag identifier with an empty value.
// GCP sanitizes both the key and the value of a label.
func instanceHasTag(inst *cpi.Instance, key, value string) bool {
	if got, ok := inst.Tags[key]; ok && got == value {
		return true
	}

	if _, ok := inst.Tags[pve.FormatTag(key, value)]; ok {
		return true
	}

	got, ok := inst.Tags[gcp.SanitizeLabel(key)]

	return ok && got == gcp.SanitizeLabel(value)
}

// readArtifacts finds the artifacts VM from state first and then by a
// provider tag query, and fills in its power state.
func readArtifacts(ctx context.Context, source blocLiveSource, entry blocinv.Entry, part *blocLiveArtifacts) {
	if !source.ArtifactsEnabled {
		part.Error = ErrArtifactsDisabled.Error()

		return
	}

	lookup, err := lookupBlocArtifacts(ctx, source.Reader, entry)

	switch {
	case errors.Is(err, errBlocLiveAmbiguousVM):
		part.Error = err.Error()

		return
	case err != nil:
		part.Error = blocinv.SafeMessage(err, entry.StatePath)

		return
	}

	if lookup == nil {
		part.Error = fmt.Sprintf("%v: %s", ErrArtifactsNotFound, entry.Name)

		return
	}

	part.VMID = lookup.VMID
	part.Endpoint = lookup.Endpoint
	part.TLSMode = lookup.TLSMode

	inst, err := source.Reader.GetInstance(ctx, lookup.VMID)
	if err != nil {
		part.Error = blocinv.SafeMessage(classifyProviderError(err, blocinv.KindProviderRequest, lookup.VMID), "")

		return
	}

	part.Power = string(inst.State)
}

// lookupBlocArtifacts resolves the artifacts VM from the bloc's state record,
// falling back to the provider's tag query when state has none. It returns nil
// with no error when neither knows of one, and an error when more than one of
// the bloc's VMs has the artifacts VM's name.
func lookupBlocArtifacts(ctx context.Context, reader blocComputeReader, entry blocinv.Entry) (*artifacts.LookupResult, error) {
	if entry.HasState && !entry.Broken && entry.StatePath != "" {
		// Read the directory of the file the inventory showed. Resolving the
		// state directory again can pick a different home when the XDG and
		// legacy homes both exist.
		manager, err := state.NewManager(filepath.Dir(entry.StatePath))
		if err != nil {
			return nil, fmt.Errorf("opening state: %w", err)
		}

		_, err = manager.Load(entry.Name)
		if err != nil {
			return nil, fmt.Errorf("loading state: %w", err)
		}

		// A nil provider makes Lookup answer from state alone.
		found, err := artifacts.Lookup(ctx, manager, nil, entry.Name)
		if err != nil {
			return nil, fmt.Errorf("looking up artifacts: %w", err)
		}

		if found != nil {
			return found, nil
		}
	}

	found, err := findOwnedVM(ctx, reader, entry.Name+"-artifacts", artifactsOwnerTags(entry.Name))
	if err != nil {
		return nil, err
	}

	if found == nil {
		return nil, nil //nolint:nilnil // no artifacts VM is a normal answer, reported by the caller
	}

	return &artifacts.LookupResult{VMID: found.ID, Name: found.Name, PrivateIP: found.PrivateIP}, nil
}

// writeBlocLiveText prints the Live section.
func writeBlocLiveText(out io.Writer, live *blocLiveReport) error {
	var text strings.Builder

	field := func(label, value string) {
		fmt.Fprintf(&text, "  %-20s %s\n", label+":", value)
	}

	text.WriteString("\nLive\n")
	field("Bastion VM", hyphenIfEmpty(live.Bastion.VMID))
	field("Bastion power", hyphenIfEmpty(live.Bastion.Power))

	if live.Bastion.Note != "" {
		field("Bastion note", live.Bastion.Note)
	}

	if live.Bastion.Error != "" {
		field("Bastion error", live.Bastion.Error)
	}

	field("Artifacts VM", hyphenIfEmpty(live.Artifacts.VMID))
	field("Artifacts power", hyphenIfEmpty(live.Artifacts.Power))
	field("Artifacts endpoint", hyphenIfEmpty(live.Artifacts.Endpoint))
	field("Artifacts TLS mode", hyphenIfEmpty(live.Artifacts.TLSMode))

	if live.Artifacts.Error != "" {
		field("Artifacts error", live.Artifacts.Error)
	}

	_, err := io.WriteString(out, text.String())
	if err != nil {
		return fmt.Errorf("writing live section: %w", err)
	}

	return nil
}
