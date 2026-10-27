package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/blocinv"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// blocLiveTimeout bounds all the provider calls one `bloc show --live` makes,
// so an unreachable provider degrades to errors in the Live section instead of
// hanging the command.
const blocLiveTimeout = 60 * time.Second

// Errors recorded in the Live section.
var (
	ErrBlocLiveNoConfig  = errors.New("no config defines this bloc, so no provider can be reached")
	ErrBlocLiveNoBastion = errors.New("no bastion id in state")
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
// which is not always the --config file, and connects its provider.
func openBlocLiveSource(ctx context.Context, blocName, configFile string) (blocLiveSource, error) {
	if configFile == "" {
		return blocLiveSource{}, ErrBlocLiveNoConfig
	}

	cfg, err := loadBlocConfiguration(configFile, blocName)
	if err != nil {
		return blocLiveSource{}, fmt.Errorf("loading config: %w", err)
	}

	iaas, region, err := determineProviderAndRegion(cfg)
	if err != nil {
		return blocLiveSource{}, fmt.Errorf("resolving provider: %w", err)
	}

	provider, err := createProvider(ctx, iaas, buildProviderConfig(cfg, region))
	if err != nil {
		return blocLiveSource{}, fmt.Errorf("creating provider: %w", err)
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
		message := blocinv.SafeMessage(err, entry.ConfigFile)
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

	readBastion(ctx, source.Reader, &report.Bastion)
	readArtifacts(ctx, source, entry, &report.Artifacts)

	return report
}

// readBastion fills in the bastion's power state from the provider.
func readBastion(ctx context.Context, reader blocComputeReader, bastion *blocLiveBastion) {
	if bastion.VMID == "" {
		bastion.Error = ErrBlocLiveNoBastion.Error()

		return
	}

	inst, err := reader.GetInstance(ctx, bastion.VMID)
	if err != nil {
		bastion.Error = fmt.Sprintf("getting instance %s: %s", bastion.VMID, blocinv.SafeMessage(err, ""))

		return
	}

	bastion.Power = string(inst.State)
}

// readArtifacts finds the artifacts VM the way `ocfp artifacts` does, state
// first and then a provider tag query, and fills in its power state.
func readArtifacts(ctx context.Context, source blocLiveSource, entry blocinv.Entry, part *blocLiveArtifacts) {
	if !source.ArtifactsEnabled {
		part.Error = ErrArtifactsDisabled.Error()

		return
	}

	lookup, err := lookupBlocArtifacts(ctx, source.Reader, entry)
	if err != nil {
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
		part.Error = fmt.Sprintf("getting instance %s: %s", lookup.VMID, blocinv.SafeMessage(err, ""))

		return
	}

	part.Power = string(inst.State)
}

// lookupBlocArtifacts resolves the artifacts VM from the bloc's state record,
// falling back to the provider's tag query when state has none. It returns nil
// with no error when neither knows of one.
func lookupBlocArtifacts(ctx context.Context, reader blocComputeReader, entry blocinv.Entry) (*artifacts.LookupResult, error) {
	if entry.HasState && !entry.Broken {
		stateDir, err := state.GetStateDir(entry.Name)
		if err != nil {
			return nil, fmt.Errorf("finding state directory: %w", err)
		}

		manager, err := state.NewManager(stateDir)
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

	vmName := entry.Name + "-artifacts"

	instances, err := reader.ListInstances(ctx, map[string]string{
		"ocfp:role": "artifacts",
		"ocfp:bloc": entry.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("listing artifacts instances by tag: %w", err)
	}

	for _, inst := range instances {
		if inst.Name == vmName {
			return &artifacts.LookupResult{VMID: inst.ID, Name: inst.Name, PrivateIP: inst.PrivateIP}, nil
		}
	}

	return nil, nil //nolint:nilnil // no artifacts VM is a normal answer, reported by the caller
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
