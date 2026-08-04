package bootstrap

import (
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/config"
	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
)

// TestSavePrivateKey_StdoutMessageUsesResolvedKeyDir verifies the
// "Private key saved to" confirmation printed by savePrivateKey shows the
// actual resolved SSH key directory (config.OcfpSSHKeyDir) rather than a
// hardcoded "~/.ocfp/<bloc>/ssh" literal, when OCFP_HOME is unset and
// XDG_DATA_HOME points at a temp directory.
func TestSavePrivateKey_StdoutMessageUsesResolvedKeyDir(t *testing.T) {
	xdgData := t.TempDir()
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_DATA_HOME", xdgData)

	const blocName = "ssh-stdout-xdg-bloc"

	sm, err := state.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("state.NewManager: %v", err)
	}

	if _, err := sm.Load(blocName); err != nil {
		t.Fatalf("sm.Load: %v", err)
	}

	m := NewManager(&config.Config{}, nil, sm, &Options{BlocName: blocName, Provider: "pve"})

	wantKeyDir := config.OcfpSSHKeyDir(blocName)

	out := captureStdout(t, func() {
		if err := m.savePrivateKey(
			"-----BEGIN OPENSSH PRIVATE KEY-----\ntest\n-----END OPENSSH PRIVATE KEY-----",
			"ssh-ed25519 AAAAtest test@xdg",
		); err != nil {
			t.Fatalf("savePrivateKey: %v", err)
		}
	})

	output := string(out)

	if !strings.Contains(output, wantKeyDir) {
		t.Errorf("savePrivateKey stdout = %q, want it to contain resolved key dir %q", output, wantKeyDir)
	}

	if strings.Contains(output, "~/.ocfp") {
		t.Errorf("savePrivateKey stdout = %q, must not contain hardcoded legacy literal %q", output, "~/.ocfp")
	}
}

// TestSaveBastionOutputs_SSHCommandUsesResolvedKeyDir verifies the
// bastion_ssh_command output persisted by saveBastionOutputs shows the
// actual resolved SSH key directory (config.OcfpSSHKeyDir) rather than a
// hardcoded "~/.ocfp/<bloc>/ssh" literal, when OCFP_HOME is unset and
// XDG_DATA_HOME points at a temp directory.
func TestSaveBastionOutputs_SSHCommandUsesResolvedKeyDir(t *testing.T) {
	xdgData := t.TempDir()
	t.Setenv("OCFP_HOME", "")
	t.Setenv("XDG_DATA_HOME", xdgData)

	const blocName = "ssh-cmd-xdg-bloc"

	sm, err := state.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("state.NewManager: %v", err)
	}

	if _, err := sm.Load(blocName); err != nil {
		t.Fatalf("sm.Load: %v", err)
	}

	m := NewManager(&config.Config{}, nil, sm, &Options{BlocName: blocName, Provider: "aws"})

	wantKeyDir := config.OcfpSSHKeyDir(blocName)

	instance := &cpi.Instance{
		ID:        "i-xdgresidual",
		Flavor:    "t3.micro",
		Image:     "ami-test",
		PrivateIP: "10.0.0.5",
		PublicIP:  "203.0.113.5",
		KeyPair:   blocName + "-keypair",
	}

	m.saveBastionOutputs(instance)

	rawCmd, err := sm.GetOutput("bastion_ssh_command")
	if err != nil {
		t.Fatalf("GetOutput(bastion_ssh_command): %v", err)
	}

	sshCommand, ok := rawCmd.(string)
	if !ok {
		t.Fatalf("bastion_ssh_command output is %T, want string", rawCmd)
	}

	if !strings.Contains(sshCommand, wantKeyDir) {
		t.Errorf("bastion_ssh_command = %q, want it to contain resolved key dir %q", sshCommand, wantKeyDir)
	}

	if strings.Contains(sshCommand, "~/.ocfp") {
		t.Errorf("bastion_ssh_command = %q, must not contain hardcoded legacy literal %q", sshCommand, "~/.ocfp")
	}
}
