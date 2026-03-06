package commands

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/ocfp/ocfp-cli-go/internal/logger"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var reservedIPPattern = regexp.MustCompile(`^reserved_.+-ocfp-\d+_(.+)_ip$`)

func newSSHConfigCmd() *cobra.Command {
	//nolint:exhaustruct // Using zero values for optional fields
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Generate SSH config entries for bloc hosts",
		Long: `Generates SSH config blocks for the bloc's bastion and internal hosts.

Output is suitable for appending to ~/.ssh/config. Internal hosts are
discovered from state outputs and configured with ProxyJump through the
bastion.`,
		Example: `  # Generate SSH config for a bloc
  ocfp ssh config --bloc production

  # Append to SSH config
  ocfp ssh config --bloc production >> ~/.ssh/config`,
		Args: cobra.NoArgs,
		RunE: runSSHConfig,
	}

	cmd.SilenceUsage = true

	return cmd
}

func runSSHConfig(_cmd *cobra.Command, _args []string) error {
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

	keyPath := sshCfg.KeyPath
	if keyPath == "" {
		keyPath, err = findSSHKey(blocName, cfg)
		if err != nil {
			return fmt.Errorf("failed to find SSH key: %w", err)
		}
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

	// Sort keys for deterministic results (lowest slot number wins)
	keys := make([]string, 0, len(outputs))
	for k := range outputs {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, key := range keys {
		matches := reservedIPPattern.FindStringSubmatch(key)
		if matches == nil {
			continue
		}

		component := matches[1]

		if component == "bastion" {
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

func generateSSHConfig(bastionIP, blocName, user, keyPath string, internalHosts map[string]string) string {
	var sb strings.Builder

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
	fmt.Fprintf(sb, "    IdentityFile %s\n", keyPath)

	if proxyJump != "" {
		fmt.Fprintf(sb, "    ProxyJump %s\n", proxyJump)
	}

	fmt.Fprint(sb, "    StrictHostKeyChecking no\n")
	fmt.Fprint(sb, "    UserKnownHostsFile /dev/null\n")
	fmt.Fprint(sb, "    LogLevel ERROR\n")
	fmt.Fprint(sb, "    IdentitiesOnly yes\n")
}

