package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	pveclient "github.com/ocfp/ocfp-cli-go/internal/cpi/pve"
)

// Errors returned by the template commands.
var (
	// ErrUnknownTemplate is returned when the requested template is not in
	// the compile-time catalog.
	ErrUnknownTemplate = errors.New("template is not in the OCFP catalog")

	// ErrTemplateProvisionNotPVE is returned when the named bloc does not
	// target Proxmox. Templates are a PVE concept; every other provider
	// resolves images from its own catalog.
	ErrTemplateProvisionNotPVE = errors.New("template provisioning requires a PVE bloc")
)

// catalogTemplateNames returns the catalog's template names, sorted, so both
// the listing and the error messages are stable rather than map-iteration
// order.
func catalogTemplateNames() []string {
	names := pveclient.CatalogTemplateNames()
	sort.Strings(names)

	return names
}

// resolveTemplateName looks a template up in the catalog, failing with a list
// of what is available.
//
// Failing here rather than at the provider matters: the catalog is
// compile-time, so a name that is not in it is almost always a typo, and the
// alternative is a provider-side "image not found" much further along.
func resolveTemplateName(name string) (pveclient.TemplateSpec, error) {
	spec, ok := pveclient.LookupCatalogSpec(name)
	if !ok {
		return pveclient.TemplateSpec{}, fmt.Errorf("%w: %q. Available: %s",
			ErrUnknownTemplate, name, strings.Join(catalogTemplateNames(), ", "))
	}

	return spec, nil
}

// NewPVETemplateCmd returns the `ocfp pve template` command group.
func NewPVETemplateCmd() *cobra.Command {
	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional cobra fields
		Use:   "template",
		Short: "Manage OCFP's PVE cloud-image templates",
		Long: `Manage the cloud-image templates OCFP clones bastion, artifacts, and
jumpbox VMs from.

The catalog is compile-time: templates cannot be added from a bloc config.`,
	}

	cmd.AddCommand(NewPVETemplateListCmd())
	cmd.AddCommand(NewPVETemplateProvisionCmd())

	return cmd
}

// NewPVETemplateListCmd returns `ocfp pve template list`.
func NewPVETemplateListCmd() *cobra.Command {
	return &cobra.Command{ //nolint:exhaustruct // Using zero values for optional cobra fields
		Use:   "list",
		Short: "List the templates OCFP knows how to build",
		RunE: func(_ *cobra.Command, _ []string) error {
			for _, name := range catalogTemplateNames() {
				spec, ok := pveclient.LookupCatalogSpec(name)
				if !ok {
					continue
				}

				role := "vanilla"
				if spec.RequireBastionUnits {
					role = "bastion (seeded)"
				}

				_, _ = fmt.Fprintf(os.Stdout, "%-36s  %-16s  %s\n", name, role, spec.SourceURL)
			}

			return nil
		},
	}
}

// NewPVETemplateProvisionCmd returns `ocfp pve template provision <name>`.
//
// This exists because a template build is slow and console-driven, and until
// now the only way to trigger one was to let bootstrap notice the image was
// missing. That is a bad place for it: a build downloads a large image and
// drives a VM over a serial console, and doing that for the first time inside
// a destructive operation, with the bastion already stopped, is how a recycle
// turns into an outage.
func NewPVETemplateProvisionCmd() *cobra.Command {
	var (
		blocName   string
		configFile string
		rebuild    bool
	)

	cmd := &cobra.Command{ //nolint:exhaustruct // Using zero values for optional cobra fields
		Use:   "provision <template-name>",
		Short: "Build a PVE template from the OCFP catalog",
		Long: `Build one of OCFP's cloud-image templates on the bloc's PVE cluster.

The build downloads the release's cloud image, creates a VM from it, and for
bastion variants boots that VM once over the serial console to seed the OCFP
firstboot and watchdog units before converting it to a template.

The command is idempotent: a template that already exists is reported and
left alone. Pass --rebuild to destroy it and build it again, which is what
you want after the units a bastion template carries have changed, since those
are baked in when the template is seeded and reach no new bastion until it is
rebuilt. Rebuilding cannot harm the guests already cloned from the template,
because OCFP clones full rather than linked.

A failed bastion seed deliberately leaves the VM stopped rather than
destroying it, so its serial console stays available for diagnosis.`,
		Example: `  ocfp pve template list
  ocfp pve template provision ubuntu-resolute-template --bloc my-bloc
  ocfp pve template provision ubuntu-resolute-bastion-template --bloc my-bloc`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPVETemplateProvision(cmd.Context(), args[0], blocName, configFile, rebuild)
		},
	}

	cmd.Flags().StringVarP(&blocName, "bloc", "b", "", "bloc whose PVE cluster to build on (required)")
	cmd.Flags().StringVarP(&configFile, "config", "c", "", "path to the bloc config file")
	cmd.Flags().BoolVar(&rebuild, "rebuild", false, "destroy the existing template and build it again")
	_ = cmd.MarkFlagRequired("bloc")

	return cmd
}

func runPVETemplateProvision(ctx context.Context, templateName, blocName, configFile string, rebuild bool) error {
	spec, err := resolveTemplateName(templateName)
	if err != nil {
		return err
	}

	if ctx == nil {
		ctx = context.Background()
	}

	cfg, err := loadBlocConfiguration(configFile, blocName)
	if err != nil {
		return err
	}

	iaas, region, err := determineProviderAndRegion(cfg)
	if err != nil {
		return err
	}

	if !strings.EqualFold(iaas, "pve") {
		return fmt.Errorf("%w: bloc %q targets %s", ErrTemplateProvisionNotPVE, blocName, iaas)
	}

	provider, err := createProvider(iaas, buildProviderConfig(cfg, region))
	if err != nil {
		return err
	}

	defer func() { _ = provider.Cleanup(ctx) }()

	pveCompute, ok := provider.ComputeManager().(*pveclient.ComputeManager)
	if !ok {
		return ErrTemplateProvisionNotPVE
	}

	_, _ = fmt.Fprintf(os.Stdout, "Provisioning template %s from %s\n", spec.Name, spec.SourceURL)

	if spec.RequireBastionUnits {
		_, _ = fmt.Fprintln(os.Stdout,
			"  This is a bastion template: the VM boots once and is seeded over the serial console, which takes several minutes.")
	}

	build := pveCompute.ProvisionTemplate

	if rebuild {
		_, _ = fmt.Fprintln(os.Stdout,
			"  Rebuilding: the existing template will be destroyed first. Guests already cloned from it are unaffected.")

		build = pveCompute.RebuildTemplate
	}

	vmid, err := build(ctx, spec.Name)
	if err != nil {
		return fmt.Errorf("provision template %s: %w", spec.Name, err)
	}

	_, _ = fmt.Fprintf(os.Stdout, "Template %s ready as VMID %d\n", spec.Name, vmid)

	return nil
}
