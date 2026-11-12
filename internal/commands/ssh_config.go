package commands

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	bastionssh "github.com/fivetwenty-io/ocfp-cli/internal/bastion/ssh"
	"github.com/fivetwenty-io/ocfp-cli/internal/logger"
	"github.com/fivetwenty-io/ocfp-cli/internal/state"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// reservedIPPattern is the legacy reserved-key matcher for the default
// "-ocfp-<n>" subnet-name shape, kept as the fallback when a key's subnet
// segment matches no name recorded in the subnet_<name>_* outputs.
var reservedIPPattern = regexp.MustCompile(`^reserved_.+-ocfp-\d+_(.+)_ip$`)

func newSSHConfigCmd() *cobra.Command {
	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Generate SSH config entries for bloc hosts",
		Long: `Generates SSH config blocks for the bloc's bastion and internal hosts.

Internal hosts are discovered from state outputs and configured with
ProxyJump through the bastion.

The bastion block checks the bastion's host key with
StrictHostKeyChecking accept-new against your default known_hosts, the
same file ocfp itself uses, and the internal blocks inherit that check
on their jump through it. The internal hosts are not checked. Blocks you
appended with an earlier version turn the check off for the bastion;
delete them and add the output again to pick up the check.

ssh keeps the first value it finds for each option, so a "Host *" block
earlier in ~/.ssh/config that sets StrictHostKeyChecking or
UserKnownHostsFile overrides these blocks and turns the bastion's check
off. Put the blocks before any "Host *" block. The simplest way is to
save the output to its own file and pull it in with an Include line at
the top of ~/.ssh/config, before any Host or Match line:

  Include ~/.ssh/config.d/ocfp-<bloc>

--key names a private key to write as IdentityFile instead of the bloc key.
When neither --key nor the bloc's SSH key is on this machine, the blocks
leave out IdentityFile and IdentitiesOnly, so your own ssh-agent, default
keys, and ssh config decide how to log in.`,
		Example: `  # Generate SSH config for a bloc
  ocfp ssh config --bloc production

  # Save to its own file, then add "Include ~/.ssh/config.d/ocfp-production"
  # at the top of ~/.ssh/config
  mkdir -p ~/.ssh/config.d
  ocfp ssh config --bloc production > ~/.ssh/config.d/ocfp-production

  # Use a specific SSH key
  ocfp ssh config --bloc production --key ~/.ssh/id_ed25519`,
		Args: cobra.NoArgs,
		RunE: runSSHConfig,
	}

	cmd.SilenceUsage = true

	cmd.Flags().String("key", "", "path to SSH private key")
	bindFlagsOnRun(cmd, map[string]string{"ssh.key": "key"})

	return cmd
}

func runSSHConfig(cmd *cobra.Command, _args []string) error {
	ctx := context.Background()
	log := logger.WithOperation("ssh-config")

	blocName := viper.GetString("bloc")
	if blocName == "" {
		return ErrBlocIsRequired
	}

	sshCfg := &sshConfig{
		BlocName: blocName,
		User:     viper.GetString("ssh.user"),
		KeyPath:  viper.GetString("ssh.key"),
	}

	cfg, provider, err := setupSSHProvider(ctx, sshCfg)
	if err != nil {
		return err
	}

	bastionIP, err := findBastionIP(ctx, provider, blocName)
	if err != nil {
		return fmt.Errorf("failed to discover bastion IP: %w", err)
	}

	keyPath, err := resolveOperatorSSHKey(sshCfg.KeyPath, blocName, cfg, cmd.ErrOrStderr())
	if err != nil {
		return err
	}

	user := sshCfg.User
	if user == "" {
		user = "ubuntu"
	}

	internalHosts := discoverInternalHosts(blocName, log)

	output := generateSSHConfig(bastionIP, blocName, user, keyPath, internalHosts)
	fmt.Print(output)

	return nil
}

func discoverInternalHosts(blocName string, log logger.Logger) map[string]string {
	stateDir, err := state.GetStateDir(blocName)
	if err != nil {
		log.Debugf("Could not get state dir: %v", err)

		return map[string]string{}
	}

	stateManager, err := state.NewManager(stateDir)
	if err != nil {
		log.Debugf("Could not create state manager: %v", err)

		return map[string]string{}
	}

	st, err := stateManager.Load(blocName)
	if err != nil {
		log.Debugf("Could not load state: %v", err)

		return map[string]string{}
	}

	return extractInternalHosts(st.Outputs, blocName)
}

func extractInternalHosts(outputs map[string]interface{}, _ string) map[string]string {
	hosts := make(map[string]string)

	subnetNames := subnetNamesFromOutputs(outputs)

	// Sort keys for deterministic results (lowest slot number wins)
	keys := make([]string, 0, len(outputs))
	for k := range outputs {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, key := range keys {
		component := reservedComponentForKey(key, subnetNames)
		if component == "" || component == "bastion" {
			continue
		}

		val := outputs[key]

		ip, ok := val.(string)
		if !ok || ip == "" {
			continue
		}

		if _, exists := hosts[component]; !exists {
			hosts[component] = ip
		}
	}

	return hosts
}

// subnetNamesFromOutputs collects the bloc's subnet names from the
// subnet_<name>_{id,cidr,index} outputs bootstrap writes at creation,
// longest name first so reservedComponentForKey's prefix match never
// truncates a name that happens to be another name's prefix.
func subnetNamesFromOutputs(outputs map[string]interface{}) []string {
	set := make(map[string]struct{})

	for key := range outputs {
		rest, found := strings.CutPrefix(key, "subnet_")
		if !found {
			continue
		}

		for _, suffix := range []string{"_id", "_cidr", "_index"} {
			if name, ok := strings.CutSuffix(rest, suffix); ok && name != "" {
				set[name] = struct{}{}

				break
			}
		}
	}

	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}

	sort.Slice(names, func(i, j int) bool {
		if len(names[i]) != len(names[j]) {
			return len(names[i]) > len(names[j])
		}

		return names[i] < names[j]
	})

	return names
}

// reservedComponentForKey extracts the component ("bosh", "ocfp_ui", ...)
// from a reserved_<subnet>_<component>_ip output key. The subnet segment is
// matched against the bloc's recorded subnet names — the only way to place
// the boundary when both segments may contain underscores — falling back to
// the legacy "-ocfp-<n>" shape for keys predating the subnet-name outputs.
// Returns "" for keys that are not reserved component IPs.
func reservedComponentForKey(key string, subnetNames []string) string {
	for _, name := range subnetNames {
		rest, found := strings.CutPrefix(key, "reserved_"+name+"_")
		if !found {
			continue
		}

		if component, ok := strings.CutSuffix(rest, "_ip"); ok && component != "" {
			return component
		}
	}

	if matches := reservedIPPattern.FindStringSubmatch(key); matches != nil {
		return matches[1]
	}

	return ""
}

// writeSSHConfigHeader writes the comment that opens the output. ssh keeps
// the first value it finds for each option, so it says where the blocks
// must go for the bastion's host key check to hold.
func writeSSHConfigHeader(sb *strings.Builder, blocName string) {
	fmt.Fprintf(sb, `# ocfp ssh config for bloc %[1]s.
# ssh keeps the first value it finds for each option, so these blocks must
# come before any "Host *" block that sets StrictHostKeyChecking or
# UserKnownHostsFile, or that block turns off the bastion's host key check.
# Save this output to its own file and pull it in near the top of
# ~/.ssh/config, before any Host or Match line, with:
#   Include ~/.ssh/config.d/ocfp-%[1]s

`, blocName)
}

func generateSSHConfig(bastionIP, blocName, user, keyPath string, internalHosts map[string]string) string {
	var sb strings.Builder

	writeSSHConfigHeader(&sb, blocName)

	writeHostBlock(&sb, blocName+"-bastion", bastionIP, user, keyPath, "")

	components := make([]string, 0, len(internalHosts))
	for component := range internalHosts {
		components = append(components, component)
	}

	sort.Strings(components)

	for _, component := range components {
		sb.WriteString("\n")
		writeHostBlock(&sb, blocName+"-"+component, internalHosts[component], user, keyPath, blocName+"-bastion")
	}

	return sb.String()
}

func writeHostBlock(sb *strings.Builder, hostAlias, hostName, user, keyPath, proxyJump string) {
	fmt.Fprintf(sb, "Host %s\n", hostAlias)
	fmt.Fprintf(sb, "    HostName %s\n", hostName)
	fmt.Fprintf(sb, "    User %s\n", user)

	// Without a bloc key, leave identity selection to the rest of the
	// user's ssh config and agent.
	if keyPath != "" {
		fmt.Fprintf(sb, "    IdentityFile %s\n", bastionssh.QuoteConfigPath(keyPath))
	}

	if proxyJump != "" {
		fmt.Fprintf(sb, "    ProxyJump %s\n", proxyJump)
	}

	// The bastion's host key is checked against the default known_hosts,
	// the file the Go client reads and writes, and the internal blocks reach
	// it through this block. The hosts behind it are rebuilt often and are
	// not checked.
	if proxyJump == "" {
		fmt.Fprint(sb, "    StrictHostKeyChecking accept-new\n")
	} else {
		fmt.Fprint(sb, "    StrictHostKeyChecking no\n")
		fmt.Fprint(sb, "    UserKnownHostsFile /dev/null\n")
	}

	fmt.Fprint(sb, "    LogLevel ERROR\n")

	if keyPath != "" {
		fmt.Fprint(sb, "    IdentitiesOnly yes\n")
	}
}
