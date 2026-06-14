package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/ocfp/ocfp-cli-go/internal/artifacts"
	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Errors returned by the artifacts subcommands.
var (
	ErrArtifactsBlocRequired = errors.New("--bloc is required for artifacts commands")
	ErrArtifactsNotFound     = errors.New("no ocfp-artifacts VM found for bloc")
	ErrUnknownArtifactsAct   = errors.New("unknown artifacts action")
	ErrArtifactsDisabled     = errors.New("artifacts feature is disabled in config; set artifacts.enabled: true")
)

// NewArtifactsCmd builds the `ocfp artifacts` command tree. Mirrors the
// shape of `ocfp bastion`: a single command that switches on the action arg.
func NewArtifactsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "artifacts <action>",
		Short: "ocfp-artifacts (RustFS S3) VM lifecycle",
		Long: `Manage the ocfp-artifacts VM that runs RustFS as an S3-compatible
blobstore for BOSH and Cloud Foundry deployments.

Actions:
  lookup    Print the resolved VM ID, IP, endpoint, and credentials.
  status    Show VM power state and endpoint metadata.
  provision Install/configure RustFS on the VM and create buckets.
  start     Power on the VM.
  stop      Gracefully shut down the VM.
  restart   Power-cycle the VM.
  destroy   Delete the VM (requires --yes).

All actions require --bloc.`,
		Example: `  ocfp artifacts lookup --bloc dev
  ocfp artifacts status --bloc dev --json
  ocfp artifacts provision --bloc dev
  ocfp artifacts start --bloc dev
  ocfp artifacts stop --bloc dev
  ocfp artifacts destroy --bloc dev --yes`,
		Args: cobra.MinimumNArgs(1),
		RunE: runArtifactsCmd,
	}

	cmd.Flags().String("bloc", "", "Bloc name (required)")
	cmd.Flags().Bool("json", false, "Emit output as JSON")
	cmd.Flags().Bool("yes", false, "Skip confirmation prompts (required for destroy)")
	cmd.Flags().Bool("dry-run", false, "Preview provision actions without executing")
	cmd.Flags().String("user", "ubuntu", "SSH username for the provision connection")
	cmd.Flags().String("key", "", "Path to SSH private key for the provision connection")
	cmd.Flags().Bool("no-proxy-jump", false, "Connect directly to the artifacts VM (use when running on the bastion or otherwise on the SDN)")

	_ = viper.BindPFlag("bloc", cmd.Flags().Lookup("bloc"))
	_ = viper.BindPFlag("ssh.user", cmd.Flags().Lookup("user"))
	_ = viper.BindPFlag("ssh.key", cmd.Flags().Lookup("key"))
	_ = viper.BindPFlag("dry-run", cmd.Flags().Lookup("dry-run"))

	return cmd
}

func runArtifactsCmd(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true

	log := logger.WithOperation("artifacts")

	blocName := viper.GetString("bloc")
	if blocName == "" {
		return ErrArtifactsBlocRequired
	}

	asJSON, _ := cmd.Flags().GetBool("json")
	confirmYes, _ := cmd.Flags().GetBool("yes")

	ctx, cleanup, err := buildArtifactsContext(cmd.Context(), blocName)
	if err != nil {
		return err
	}
	defer cleanup()

	action := args[0]

	switch action {
	case "lookup":
		return artifactsLookup(ctx, asJSON)
	case "status":
		return artifactsStatus(ctx, asJSON)
	case "provision":
		return artifactsProvision(cmd, ctx, log)
	case "start":
		return artifactsLifecycle(ctx, "start")
	case "stop":
		return artifactsLifecycle(ctx, "stop")
	case "restart":
		return artifactsLifecycle(ctx, "restart")
	case "destroy":
		if !confirmYes {
			return errors.New("destroy is destructive; pass --yes to confirm")
		}

		return artifactsDestroy(ctx, log)
	default:
		return fmt.Errorf("%w: %s", ErrUnknownArtifactsAct, action)
	}
}

// artifactsContext holds the resolved per-command dependencies. Built once
// per command invocation to avoid threading them through every action.
type artifactsContext struct {
	parent   context.Context
	blocName string
	cfg      *config.Config
	provider cpi.Provider
	state    *state.Manager
	lookup   *artifacts.LookupResult
}

func buildArtifactsContext(parent context.Context, blocName string) (*artifactsContext, func(), error) {
	if parent == nil {
		parent = context.Background()
	}

	cfg, err := loadBlocConfiguration(viper.GetString("config"), blocName)
	if err != nil {
		return nil, nil, fmt.Errorf("loading config: %w", err)
	}

	if !cfg.Artifacts.Enabled {
		return nil, nil, ErrArtifactsDisabled
	}

	iaas, region, err := determineProviderAndRegion(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving provider: %w", err)
	}

	provider, err := createProvider(iaas, buildProviderConfig(cfg, region))
	if err != nil {
		return nil, nil, fmt.Errorf("creating provider: %w", err)
	}

	cleanup := func() {
		_ = provider.Cleanup(context.Background())
	}

	sm, err := createStateManager(blocName)
	if err != nil {
		cleanup()

		return nil, nil, fmt.Errorf("creating state manager: %w", err)
	}

	_, err = sm.Load(blocName)
	if err != nil {
		// State not yet initialized is non-fatal for lookup/status; we'll
		// surface the absence as "not found" downstream.
		_, _ = fmt.Fprintf(os.Stderr, "warning: loading state failed: %v\n", err)
	}

	lr, err := artifacts.Lookup(parent, sm, provider, blocName)
	if err != nil {
		cleanup()

		return nil, nil, err
	}

	return &artifactsContext{
		parent:   parent,
		blocName: blocName,
		cfg:      cfg,
		provider: provider,
		state:    sm,
		lookup:   lr,
	}, cleanup, nil
}

func artifactsLookup(ac *artifactsContext, asJSON bool) error {
	if ac.lookup == nil {
		return fmt.Errorf("%w: %s", ErrArtifactsNotFound, ac.blocName)
	}

	if asJSON {
		out, err := json.MarshalIndent(ac.lookup, "", "  ")
		if err != nil {
			return err
		}

		fmt.Println(string(out))

		return nil
	}

	fmt.Printf("Name:       %s\n", ac.lookup.Name)
	fmt.Printf("VM ID:      %s\n", ac.lookup.VMID)
	fmt.Printf("Private IP: %s\n", ac.lookup.PrivateIP)
	fmt.Printf("Endpoint:   %s\n", ac.lookup.Endpoint)
	fmt.Printf("TLS Mode:   %s\n", ac.lookup.TLSMode)
	fmt.Printf("Dataset:    %s\n", ac.lookup.ZFSDataset)

	return nil
}

func artifactsStatus(ac *artifactsContext, asJSON bool) error {
	if ac.lookup == nil {
		return fmt.Errorf("%w: %s", ErrArtifactsNotFound, ac.blocName)
	}

	inst, err := ac.provider.ComputeManager().GetInstance(ac.parent, ac.lookup.VMID)
	if err != nil {
		return fmt.Errorf("getting instance %s: %w", ac.lookup.VMID, err)
	}

	report := map[string]any{
		"name":       ac.lookup.Name,
		"vm_id":      ac.lookup.VMID,
		"state":      string(inst.State),
		"private_ip": ac.lookup.PrivateIP,
		"endpoint":   ac.lookup.Endpoint,
		"tls_mode":   ac.lookup.TLSMode,
		"dataset":    ac.lookup.ZFSDataset,
	}

	if asJSON {
		out, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}

		fmt.Println(string(out))

		return nil
	}

	for _, k := range []string{"name", "vm_id", "state", "private_ip", "endpoint", "tls_mode", "dataset"} {
		fmt.Printf("%-12s %v\n", k+":", report[k])
	}

	return nil
}

func artifactsLifecycle(ac *artifactsContext, op string) error {
	if ac.lookup == nil {
		return fmt.Errorf("%w: %s", ErrArtifactsNotFound, ac.blocName)
	}

	cm := ac.provider.ComputeManager()

	switch op {
	case "start":
		err := cm.StartInstance(ac.parent, ac.lookup.VMID)
		if err != nil {
			return fmt.Errorf("starting %s: %w", ac.lookup.VMID, err)
		}

		fmt.Printf("✓ start requested on %s (%s)\n", ac.lookup.Name, ac.lookup.VMID)
	case "stop":
		err := cm.StopInstance(ac.parent, ac.lookup.VMID)
		if err != nil {
			return fmt.Errorf("stopping %s: %w", ac.lookup.VMID, err)
		}

		fmt.Printf("✓ stop requested on %s (%s)\n", ac.lookup.Name, ac.lookup.VMID)
	case "restart":
		err := cm.RebootInstance(ac.parent, ac.lookup.VMID)
		if err != nil {
			return fmt.Errorf("rebooting %s: %w", ac.lookup.VMID, err)
		}

		fmt.Printf("✓ restart requested on %s (%s)\n", ac.lookup.Name, ac.lookup.VMID)
	default:
		return fmt.Errorf("%w: %s", ErrUnknownArtifactsAct, op)
	}

	return nil
}

func artifactsDestroy(ac *artifactsContext, log logger.Logger) error {
	if ac.lookup == nil {
		return fmt.Errorf("%w: %s", ErrArtifactsNotFound, ac.blocName)
	}

	log.Infof("destroying artifacts VM %s (id=%s)", ac.lookup.Name, ac.lookup.VMID)

	err := ac.provider.ComputeManager().DeleteInstance(ac.parent, ac.lookup.VMID)
	if err != nil {
		return fmt.Errorf("deleting instance %s: %w", ac.lookup.VMID, err)
	}

	// Delete the attached data volume so we don't leak storage on the PVE
	// node. Best-effort: the volume may already be cleaned up by the same
	// DeleteInstance call on some providers.
	if ac.lookup.DataVolumeID != "" {
		err = ac.provider.StorageManager().DeleteVolume(ac.parent, ac.lookup.DataVolumeID)
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "warning: deleting data volume %s: %v\n", ac.lookup.DataVolumeID, err)
		}
	}

	// Remove resource entry from state. Best-effort: a missing state file is
	// acceptable here because the VM is already gone.
	err = ac.state.RemoveResource("artifacts", ac.lookup.Name)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "warning: clearing artifacts state entry: %v\n", err)
	}

	_ = ac.state.Save()

	fmt.Printf("✓ destroyed %s\n", ac.lookup.Name)

	return nil
}
