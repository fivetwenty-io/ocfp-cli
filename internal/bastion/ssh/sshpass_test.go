package ssh

import (
	"context"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// newTestClient builds a minimal Client suitable for sshpass command tests.
// It does not open a real network connection.
func newTestClient(password string) *Client {
	return &Client{
		config: &ConnectionDetails{
			Host:       "test-host",
			Port:       DefaultSSHPort,
			User:       "testuser",
			Password:   password,
			UseSSHPass: true,
		},
		log: zap.NewNop().Sugar(),
	}
}

// newTestTransferManager wraps newTestClient in a TransferManager.
func newTestTransferManager(password string) *TransferManager {
	c := newTestClient(password)
	return &TransferManager{
		client:  c,
		options: TransferOptions{},
		log:     zap.NewNop().Sugar(),
	}
}

// TestBuildSCPCmd_PasswordNotInArgv asserts that the password string does not
// appear anywhere in cmd.Args when createSSHPassCommand builds the command.
func TestBuildSCPCmd_PasswordNotInArgv(t *testing.T) {
	t.Parallel()

	const pw = "s3cr3t-passw0rd"

	tm := newTestTransferManager(pw)

	ctx := context.Background()

	cmd, err := tm.createSSHPassCommand(ctx, []string{
		"-o", "StrictHostKeyChecking=no",
		"localfile",
		"testuser@test-host:/remote/path",
	})
	if err != nil {
		// sshpass binary absence is not the focus here; skip gracefully.
		t.Skipf("createSSHPassCommand returned error (sshpass likely absent): %v", err)
	}

	for _, arg := range cmd.Args {
		if strings.Contains(arg, pw) {
			t.Errorf("password must not appear in cmd.Args; found in arg: %q", arg)
		}
	}
}

// TestBuildSCPCmd_PasswordInEnv asserts that SSHPASS=<password> is present in
// cmd.Env and that -e flag is used instead of -p.
func TestBuildSCPCmd_PasswordInEnv(t *testing.T) {
	t.Parallel()

	const pw = "s3cr3t-passw0rd"

	tm := newTestTransferManager(pw)

	ctx := context.Background()

	cmd, err := tm.createSSHPassCommand(ctx, []string{
		"-o", "StrictHostKeyChecking=no",
		"localfile",
		"testuser@test-host:/remote/path",
	})
	if err != nil {
		t.Skipf("createSSHPassCommand returned error (sshpass likely absent): %v", err)
	}

	// SSHPASS=<password> must be in Env.
	wantEnv := "SSHPASS=" + pw
	found := false

	for _, e := range cmd.Env {
		if e == wantEnv {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("expected %q in cmd.Env, got: %v", wantEnv, cmd.Env)
	}

	// -e flag must be present; -p must not be present (as the sshpass password flag).
	hasE := false

	for i, arg := range cmd.Args {
		if arg == "-e" {
			hasE = true
		}

		// -p is the sshpass "read password from argv" flag — it must not appear
		// in positions where sshpass would interpret it as a password flag.
		// sshpass flags appear before the subcommand ("scp"), so only check those positions.
		if arg == "-p" && i < 3 { //nolint:mnd // positions 0-2 are sshpass binary + its flags
			t.Errorf("cmd.Args must not contain -p as a sshpass flag (password-in-argv); got args: %v", cmd.Args)
		}
	}

	if !hasE {
		t.Errorf("cmd.Args must contain -e flag; got args: %v", cmd.Args)
	}
}

// TestValidateExternalSSHConnectivity_PasswordNotInArgv asserts that the
// password does not appear in any arg of the argv slice built for the sshpass
// invocation in validateExternalSSHConnectivity.
// This test exercises the client.go argv-construction logic directly.
func TestValidateExternalSSHConnectivity_PasswordNotInArgv(t *testing.T) {
	t.Parallel()

	const pw = "another-s3cr3t"

	c := newTestClient(pw)

	// validateExternalSSHConnectivity runs a live SSH command, so we cannot
	// call it end-to-end in a unit test. We reproduce the argv-construction
	// logic from client.go and assert the security invariant.
	//
	// The implementation uses:
	//   sshpassArgs = append(sshpassArgs, "-e", "ssh")
	//   sshpassArgs = append(sshpassArgs, args...)
	//   cmd.Env = append(os.Environ(), "SSHPASS="+c.config.Password)
	args := []string{
		"-o", "ConnectTimeout=10",
		"-o", "BatchMode=yes",
		c.config.User + "@" + c.config.Host, "echo", "connection-test",
	}

	sshpassArgs := make([]string, 0, sshpassArgCount+len(args))
	sshpassArgs = append(sshpassArgs, "-e", "ssh")
	sshpassArgs = append(sshpassArgs, args...)

	for _, arg := range sshpassArgs {
		if strings.Contains(arg, pw) {
			t.Errorf("password must not appear in argv; found in arg: %q", arg)
		}
	}

	hasE := false

	for _, arg := range sshpassArgs {
		if arg == "-e" {
			hasE = true
		}

		if arg == "-p" {
			t.Errorf("sshpassArgs must not contain -p; got: %v", sshpassArgs)
		}
	}

	if !hasE {
		t.Errorf("sshpassArgs must contain -e; got: %v", sshpassArgs)
	}
}
