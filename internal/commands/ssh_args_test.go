package commands

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifySSHArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		args            []string
		expectedTarget  string
		expectedSSHArgs []string
		expectedCommand []string
	}{
		{
			name:            "EmptyArgs",
			args:            []string{},
			expectedTarget:  "bastion",
			expectedSSHArgs: []string{},
			expectedCommand: []string{},
		},
		{
			name:            "OnlyTarget",
			args:            []string{"myhost"},
			expectedTarget:  "myhost",
			expectedSSHArgs: []string{},
			expectedCommand: []string{},
		},
		{
			name:            "TargetAndCommand",
			args:            []string{"myhost", "ls", "-la"},
			expectedTarget:  "myhost",
			expectedSSHArgs: []string{},
			expectedCommand: []string{"ls", "-la"},
		},
		{
			name:            "OnlyCommand",
			args:            []string{"hostname"},
			expectedTarget:  "hostname",
			expectedSSHArgs: []string{},
			expectedCommand: []string{},
		},
		{
			name:            "MultipleCommandArgs",
			args:            []string{"bastion", "ls", ";", "hostname", ";", "echo", "$OCFP_BLOC"},
			expectedTarget:  "bastion",
			expectedSSHArgs: []string{},
			expectedCommand: []string{"ls", ";", "hostname", ";", "echo", "$OCFP_BLOC"},
		},
		{
			name:            "SSHFlagOnly",
			args:            []string{"-L", "8080:localhost:80"},
			expectedTarget:  "bastion",
			expectedSSHArgs: []string{"-L", "8080:localhost:80"},
			expectedCommand: []string{},
		},
		{
			name:            "SSHFlagWithTarget",
			args:            []string{"-L", "8080:localhost:80", "myhost"},
			expectedTarget:  "myhost",
			expectedSSHArgs: []string{"-L", "8080:localhost:80"},
			expectedCommand: []string{},
		},
		{
			name:            "SSHFlagWithTargetAndCommand",
			args:            []string{"-L", "8080:localhost:80", "myhost", "uptime"},
			expectedTarget:  "myhost",
			expectedSSHArgs: []string{"-L", "8080:localhost:80"},
			expectedCommand: []string{"uptime"},
		},
		{
			name:            "MultipleFlagsWithTargetAndCommand",
			args:            []string{"-L", "8080:localhost:80", "-D", "1080", "myhost", "ls", "/tmp"},
			expectedTarget:  "myhost",
			expectedSSHArgs: []string{"-L", "8080:localhost:80", "-D", "1080"},
			expectedCommand: []string{"ls", "/tmp"},
		},
		{
			name:            "VerboseFlag",
			args:            []string{"-v", "bastion", "hostname"},
			expectedTarget:  "bastion",
			expectedSSHArgs: []string{"-v"},
			expectedCommand: []string{"hostname"},
		},
		{
			name:            "MultipleVerboseLevels",
			args:            []string{"-vvv", "bastion"},
			expectedTarget:  "bastion",
			expectedSSHArgs: []string{"-vvv"},
			expectedCommand: []string{},
		},
		{
			name:            "PortFlag",
			args:            []string{"-p", "2222", "bastion"},
			expectedTarget:  "bastion",
			expectedSSHArgs: []string{"-p", "2222"},
			expectedCommand: []string{},
		},
		{
			name:            "ConfigOption",
			args:            []string{"-o", "ServerAliveInterval=60", "bastion", "uptime"},
			expectedTarget:  "bastion",
			expectedSSHArgs: []string{"-o", "ServerAliveInterval=60"},
			expectedCommand: []string{"uptime"},
		},
		{
			name:            "RemotePortForwarding",
			args:            []string{"-R", "9090:localhost:8080", "bastion"},
			expectedTarget:  "bastion",
			expectedSSHArgs: []string{"-R", "9090:localhost:8080"},
			expectedCommand: []string{},
		},
		{
			name:            "DynamicPortForwarding",
			args:            []string{"-D", "1080", "bastion"},
			expectedTarget:  "bastion",
			expectedSSHArgs: []string{"-D", "1080"},
			expectedCommand: []string{},
		},
		{
			name:            "ComplexCommand",
			args:            []string{"bastion", "echo", "'Hello World'"},
			expectedTarget:  "bastion",
			expectedSSHArgs: []string{},
			expectedCommand: []string{"echo", "'Hello World'"},
		},
		{
			name:            "CommandWithPipe",
			args:            []string{"bastion", "ps", "aux", "|", "grep", "bash"},
			expectedTarget:  "bastion",
			expectedSSHArgs: []string{},
			expectedCommand: []string{"ps", "aux", "|", "grep", "bash"},
		},
		{
			name:            "FlagNoValueSeparate",
			args:            []string{"-4", "bastion", "hostname"},
			expectedTarget:  "bastion",
			expectedSSHArgs: []string{"-4"},
			expectedCommand: []string{"hostname"},
		},
		{
			name:            "MultipleFlagsNoTarget",
			args:            []string{"-L", "8080:localhost:80", "-v"},
			expectedTarget:  "bastion",
			expectedSSHArgs: []string{"-L", "8080:localhost:80", "-v"},
			expectedCommand: []string{},
		},
		{
			name:            "QuotedCommand",
			args:            []string{"bastion", "echo", "\"test message\""},
			expectedTarget:  "bastion",
			expectedSSHArgs: []string{},
			expectedCommand: []string{"echo", "\"test message\""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			target, sshArgs, command := classifySSHArguments(tt.args)

			assert.Equal(t, tt.expectedTarget, target, "Target mismatch")
			assert.Equal(t, tt.expectedSSHArgs, sshArgs, "SSH args mismatch")
			assert.Equal(t, tt.expectedCommand, command, "Command mismatch")
		})
	}
}

func TestBuildSSHCommandWithArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		host        string
		user        string
		keyPath     string
		extraOpts   string
		sshArgs     []string
		command     []string
		expectContains []string
		expectNotContains []string
	}{
		{
			name:        "BasicInteractiveSession",
			host:        "10.0.0.1",
			user:        "ubuntu",
			keyPath:     "/path/to/key",
			extraOpts:   "",
			sshArgs:     []string{},
			command:     []string{},
			expectContains: []string{"ssh", "-i", "/path/to/key", "ubuntu@10.0.0.1"},
		},
		{
			name:        "WithSimpleCommand",
			host:        "10.0.0.1",
			user:        "ubuntu",
			keyPath:     "/path/to/key",
			extraOpts:   "",
			sshArgs:     []string{},
			command:     []string{"hostname"},
			expectContains: []string{"ssh", "ubuntu@10.0.0.1", "hostname"},
		},
		{
			name:        "WithMultipleCommands",
			host:        "10.0.0.1",
			user:        "ubuntu",
			keyPath:     "/path/to/key",
			extraOpts:   "",
			sshArgs:     []string{},
			command:     []string{"ls", ";", "hostname"},
			expectContains: []string{"ssh", "ubuntu@10.0.0.1", "ls", ";", "hostname"},
		},
		{
			name:        "WithLocalPortForwarding",
			host:        "10.0.0.1",
			user:        "ubuntu",
			keyPath:     "/path/to/key",
			extraOpts:   "",
			sshArgs:     []string{"-L", "8080:localhost:80"},
			command:     []string{},
			expectContains: []string{"ssh", "-L", "8080:localhost:80", "ubuntu@10.0.0.1"},
		},
		{
			name:        "WithDynamicPortForwarding",
			host:        "10.0.0.1",
			user:        "ubuntu",
			keyPath:     "/path/to/key",
			extraOpts:   "",
			sshArgs:     []string{"-D", "1080"},
			command:     []string{},
			expectContains: []string{"ssh", "-D", "1080", "ubuntu@10.0.0.1"},
		},
		{
			name:        "WithRemotePortForwarding",
			host:        "10.0.0.1",
			user:        "ubuntu",
			keyPath:     "/path/to/key",
			extraOpts:   "",
			sshArgs:     []string{"-R", "9090:localhost:8080"},
			command:     []string{},
			expectContains: []string{"ssh", "-R", "9090:localhost:8080", "ubuntu@10.0.0.1"},
		},
		{
			name:        "WithVerboseFlag",
			host:        "10.0.0.1",
			user:        "ubuntu",
			keyPath:     "/path/to/key",
			extraOpts:   "",
			sshArgs:     []string{"-v"},
			command:     []string{},
			expectContains: []string{"ssh", "-v", "ubuntu@10.0.0.1"},
		},
		{
			name:        "WithMultipleFlagsAndCommand",
			host:        "10.0.0.1",
			user:        "ubuntu",
			keyPath:     "/path/to/key",
			extraOpts:   "",
			sshArgs:     []string{"-L", "8080:localhost:80", "-v"},
			command:     []string{"uptime"},
			expectContains: []string{"ssh", "-L", "8080:localhost:80", "-v", "ubuntu@10.0.0.1", "uptime"},
		},
		{
			name:        "UnsafeFlagFiltered",
			host:        "10.0.0.1",
			user:        "ubuntu",
			keyPath:     "/path/to/key",
			extraOpts:   "",
			sshArgs:     []string{"-X", "display"},
			command:     []string{},
			expectNotContains: []string{"-X"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := buildSSHCommand(tt.host, tt.user, tt.keyPath, tt.extraOpts, tt.sshArgs, tt.command)

			for _, expected := range tt.expectContains {
				assert.Contains(t, result, expected, "Expected command to contain: %s", expected)
			}

			for _, notExpected := range tt.expectNotContains {
				assert.NotContains(t, result, notExpected, "Expected command to NOT contain: %s", notExpected)
			}
		})
	}
}
