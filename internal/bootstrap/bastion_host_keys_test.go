package bootstrap_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/bootstrap"
	"github.com/fivetwenty-io/ocfp-cli/internal/cpi"
	"github.com/fivetwenty-io/ocfp-cli/internal/state"
)

// The tests in this file share the package's test known_hosts file, so none
// of them runs in parallel.

const (
	staleHostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"
	otherHostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIfZyzrfwaHE2lc5SfL2mIQmixCgxTm1yrJPoSuc9vJb"
)

// useKnownHosts fills the package's test known_hosts file with content and
// removes it when the test ends.
func useKnownHosts(t *testing.T, content string) string {
	t.Helper()

	knownHosts, err := bootstrap.KnownHostsFile()
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Remove(knownHosts) })

	err = os.MkdirAll(filepath.Dir(knownHosts), 0o700)
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(knownHosts, []byte(content), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	return knownHosts
}

func assertKnownHosts(t *testing.T, path, want string) {
	t.Helper()

	got, err := os.ReadFile(path) // #nosec G304 -- test temp path
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != want {
		t.Errorf("known_hosts = %q, want %q", got, want)
	}
}

// A freshly created bastion has new host keys. Entries left in known_hosts
// by an earlier machine at the same addresses must go before anything
// connects, or the strict host key check would refuse the new bastion.
// Entries for other hosts stay as they are.
//
//nolint:paralleltest // shares the package test known_hosts file
func TestCreateBastion_ForgetsStaleHostKeys(t *testing.T) {
	unrelated := "10.4.0.11 " + otherHostKey + "\n"
	knownHosts := useKnownHosts(t, "10.4.0.10 "+staleHostKey+"\n"+unrelated+"100.64.0.9 "+staleHostKey+"\n")

	manager, _, _, cfg := setupComputeTest(t, "stackit")
	cfg.BastionIP = "100.64.0.9"

	err := manager.CreateBastion(context.Background())
	if err != nil {
		t.Fatalf("CreateBastion failed: %v", err)
	}

	assertKnownHosts(t, knownHosts, unrelated)
}

// STACKIT can find the bloc's reserved public IP in the cloud by its labels
// rather than in state. That address is the one operators dial, so its old
// entry must go as well.
//
//nolint:paralleltest // shares the package test known_hosts file
func TestCreateBastion_ForgetsStaleKeyForLabelledPublicIP(t *testing.T) {
	unrelated := "192.0.2.8 " + otherHostKey + "\n"
	knownHosts := useKnownHosts(t, "192.0.2.7 "+staleHostKey+"\n"+unrelated)

	manager, _, network, _ := setupComputeTest(t, "stackit")
	network.publicIPs = []*cpi.PublicIP{{
		ID:      "pip-bastion",
		Address: "192.0.2.7",
		Labels:  map[string]string{"job": "bastion", "index": "0"},
	}}

	err := manager.CreateBastion(context.Background())
	if err != nil {
		t.Fatalf("CreateBastion failed: %v", err)
	}

	assertKnownHosts(t, knownHosts, unrelated)
}

// When the provider creates the VM but its setup then fails, the VM still
// holds the bastion's addresses with new host keys. The old entries go at
// once, because a rerun stops on the recorded failure and never reaches the
// end of a successful creation.
//
//nolint:paralleltest // shares the package test known_hosts file
func TestCreateBastion_ForgetsStaleHostKeysWhenSetupFails(t *testing.T) {
	unrelated := "10.4.0.11 " + otherHostKey + "\n"
	knownHosts := useKnownHosts(t, "10.4.0.10 "+staleHostKey+"\n"+unrelated)

	manager, compute, _, _ := setupComputeTest(t, "stackit")
	compute.failAfterCreate = true

	err := manager.CreateBastion(context.Background())
	if err == nil {
		t.Fatal("CreateBastion succeeded although setup failed")
	}

	assertKnownHosts(t, knownHosts, unrelated)
}

// Adopting a bastion that already exists, from state or found in the cloud,
// creates nothing. Its host keys are the ones on record, so known_hosts must
// stay exactly as it is.
//
//nolint:paralleltest // shares the package test known_hosts file
func TestCreateBastion_AdoptingExistingBastionKeepsKnownHosts(t *testing.T) {
	content := "10.4.0.10 " + staleHostKey + "\n100.64.0.9 " + staleHostKey + "\n"

	existing := &cpi.Instance{
		ID:        "inst-existing",
		Name:      "prod-bastion",
		State:     cpi.ResourceStateActive,
		PrivateIP: "10.4.0.10",
		Tags:      map[string]string{"bloc": "prod", "managed-by": "ocfp"},
	}

	cases := map[string]func(*testing.T, *state.Manager, *fakeComputeEnhanced){
		"recorded in state": func(t *testing.T, sm *state.Manager, _ *fakeComputeEnhanced) {
			t.Helper()

			err := sm.AddResource(&state.Resource{
				ID:         existing.ID,
				Type:       state.ResourceTypeInstance,
				Name:       existing.Name,
				Properties: map[string]interface{}{"private_ip": existing.PrivateIP},
			})
			if err != nil {
				t.Fatal(err)
			}
		},
		"found in the cloud": func(_ *testing.T, _ *state.Manager, compute *fakeComputeEnhanced) {
			compute.instances[existing.ID] = existing
		},
	}

	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			knownHosts := useKnownHosts(t, content)

			manager, compute, _, cfg := setupComputeTest(t, "stackit")
			cfg.BastionIP = "100.64.0.9"
			arrange(t, manager.StateManager(), compute)

			err := manager.CreateBastion(context.Background())
			if err != nil {
				t.Fatalf("CreateBastion failed: %v", err)
			}

			if compute.lastReq != nil {
				t.Fatal("CreateBastion created an instance although one exists")
			}

			assertKnownHosts(t, knownHosts, content)
		})
	}
}

// PVE_BASTION_IP, often a Tailscale name, and AWS_BASTION_IP name the
// bastion that provider's commands dial, so their entries go with the rest.
// Each is read only for its own provider.
//
//nolint:paralleltest // sets environment variables and shares known_hosts
func TestForgetBastionHostKeys_ClearsEnvironmentBastionHosts(t *testing.T) {
	cases := []struct {
		provider, env, host string
		cleared             bool
	}{
		{"pve", "PVE_BASTION_IP", "prod-bastion.tailnet.ts.net", true},
		{"aws", "AWS_BASTION_IP", "198.51.100.4", true},
		{"aws", "PVE_BASTION_IP", "prod-bastion.tailnet.ts.net", false},
		{"pve", "AWS_BASTION_IP", "198.51.100.4", false},
	}

	for _, tc := range cases {
		t.Run(tc.provider+"/"+tc.env, func(t *testing.T) {
			t.Setenv("PVE_BASTION_IP", "")
			t.Setenv("AWS_BASTION_IP", "")
			t.Setenv(tc.env, tc.host)

			entry := tc.host + " " + staleHostKey + "\n"
			knownHosts := useKnownHosts(t, entry)

			manager, _, _, _ := setupComputeTest(t, tc.provider)
			manager.ForgetBastionHostKeys(&cpi.Instance{ID: "inst-new", PrivateIP: "10.4.0.10"})

			want := entry
			if tc.cleared {
				want = ""
			}

			assertKnownHosts(t, knownHosts, want)
		})
	}
}
