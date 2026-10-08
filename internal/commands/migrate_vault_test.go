package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocfp/ocfp-cli-go/internal/config"
)

// fakeMigrateVaults scripts what config migrate's liveness probes find: what
// answers on each API port, and which tmux sessions exist. Anything not
// listed is stopped and absent. It records every port and session asked
// about.
type fakeMigrateVaults struct {
	mu       sync.Mutex
	ports    map[string]vaultProbeState
	sessions map[string]bool
	probed   []string
	asked    []string
}

func (f *fakeMigrateVaults) probes() inceptionLivenessProbes {
	return inceptionLivenessProbes{
		probe: func(_ context.Context, addr string) vaultProbe {
			f.mu.Lock()
			defer f.mu.Unlock()

			port := addr[strings.LastIndex(addr, ":")+1:]
			f.probed = append(f.probed, port)

			state, ok := f.ports[port]
			if !ok {
				return vaultProbe{state: vaultProbeStopped}
			}

			return vaultProbe{state: state, initialized: true}
		},
		hasSession: func(_ context.Context, paths map[string]string) bool {
			f.mu.Lock()
			defer f.mu.Unlock()

			f.asked = append(f.asked, paths["tmuxSession"])

			return f.sessions[paths["tmuxSession"]]
		},
	}
}

// useMigrateVaults installs fake as config migrate's liveness probes for the
// rest of the test.
func useMigrateVaults(t *testing.T, fake *fakeMigrateVaults) {
	t.Helper()

	saved := migrateVaultProbes
	migrateVaultProbes = fake.probes()

	t.Cleanup(func() { migrateVaultProbes = saved })
}

const migrateVaultTestBloc = "ocfp-lab-migrate"

func migrateVaultTestPort() string {
	return strconv.Itoa(config.InceptionVaultPort(migrateVaultTestBloc))
}

// writeLegacyBlocVault lays out a legacy bloc directory with a raft vault,
// its keys, and its logs.
func writeLegacyBlocVault(t *testing.T, legacyDir string) {
	t.Helper()

	blocDir := filepath.Join(legacyDir, migrateVaultTestBloc)
	writeMigrateTestFile(t, filepath.Join(blocDir, "vault", "data", "vault.db"), "raft")
	writeMigrateTestFile(t, filepath.Join(blocDir, "vault", "root.key"), "root")
	writeMigrateTestFile(t, filepath.Join(blocDir, "vault", "unseal.keys"), "unseal")
	writeMigrateTestFile(t, filepath.Join(blocDir, "logs", "vault", "vault-inception.log"), "log")
}

// requireLegacyBlocUntouched checks that nothing of the bloc moved.
func requireLegacyBlocUntouched(t *testing.T, legacyDir string) {
	t.Helper()

	blocDir := filepath.Join(legacyDir, migrateVaultTestBloc)
	for _, rel := range []string{"vault/data/vault.db", "vault/root.key", "vault/unseal.keys", "logs/vault/vault-inception.log"} {
		mustExist(t, filepath.Join(blocDir, rel))
	}

	mustExist(t, filepath.Join(legacyDir, "config.yml"))
	mustNotExist(t, filepath.Join(config.DataHome(), migrateVaultTestBloc))
	mustNotExist(t, filepath.Join(config.StateHome(), migrateVaultTestBloc))
	mustNotExist(t, filepath.Join(config.ConfigHome(), "config.yml"))
}

// A running vault would have its data directory moved out from under it, so
// config migrate refuses before it moves anything, on a dry run too, and
// says how to stop the vault first.
func TestRunMigrate_RunningBlocVaultRefuses(t *testing.T) {
	port := migrateVaultTestPort()
	session := migrateVaultTestBloc + "-inception-vault"

	cases := map[string]struct {
		fake *fakeMigrateVaults
		want []string
	}{
		"vault on the port and its session": {
			fake: &fakeMigrateVaults{
				ports:    map[string]vaultProbeState{port: vaultProbeVault},
				sessions: map[string]bool{session: true},
			},
			want: []string{"a vault answers on port " + port, "tmux session " + session + " exists"},
		},
		"vault on the port without a session": {
			fake: &fakeMigrateVaults{ports: map[string]vaultProbeState{port: vaultProbeVault}},
			want: []string{"a vault answers on port " + port, "lsof -nP -iTCP:" + port + " -sTCP:LISTEN"},
		},
		"session alone": {
			fake: &fakeMigrateVaults{sessions: map[string]bool{session: true}},
			want: []string{"tmux session " + session + " exists"},
		},
		"something else on the port": {
			fake: &fakeMigrateVaults{ports: map[string]vaultProbeState{port: vaultProbeStranger}},
			want: []string{"something answers on port " + port},
		},
	}

	for name, tc := range cases {
		for _, dryRun := range []bool{false, true} {
			t.Run(name+"/dry-run="+strconv.FormatBool(dryRun), func(t *testing.T) {
				home := isolateXDGEnv(t)
				legacyDir := filepath.Join(home, ".ocfp")

				writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: test\n")
				writeLegacyBlocVault(t, legacyDir)
				useMigrateVaults(t, tc.fake)

				err := runMigrate(dryRun)
				if !errors.Is(err, ErrMigrateInceptionVaultRunning) {
					t.Fatalf("runMigrate(%v) error = %v, want ErrMigrateInceptionVaultRunning", dryRun, err)
				}

				wants := append([]string{"bloc " + migrateVaultTestBloc, "tmux kill-session -t " + session}, tc.want...)
				for _, want := range wants {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not mention %q", err, want)
					}
				}

				requireLegacyBlocUntouched(t, legacyDir)
			})
		}
	}
}

// The vault check runs before the live-process guard, which prunes stale
// command locks as it scans, so a refusal leaves even those in place.
func TestRunMigrate_RunningBlocVaultRefusesBeforePruningLocks(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")

	writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: test\n")
	writeLegacyBlocVault(t, legacyDir)
	writeMigrateLockFile(t, legacyDir, deadPID(t), time.Now())

	locks, err := filepath.Glob(filepath.Join(legacyDir, ".active", "*.lock"))
	if err != nil || len(locks) != 1 {
		t.Fatalf("stale lock setup: %v, %v", locks, err)
	}

	useMigrateVaults(t, &fakeMigrateVaults{ports: map[string]vaultProbeState{migrateVaultTestPort(): vaultProbeVault}})

	err = runMigrate(false)
	if !errors.Is(err, ErrMigrateInceptionVaultRunning) {
		t.Fatalf("runMigrate() error = %v, want ErrMigrateInceptionVaultRunning", err)
	}

	mustExist(t, locks[0])
	requireLegacyBlocUntouched(t, legacyDir)
}

// With every vault stopped, the bloc moves as before, and the probes asked
// about the bloc's own port and session.
func TestRunMigrate_StoppedBlocVaultMigrates(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")

	writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: test\n")
	writeLegacyBlocVault(t, legacyDir)

	fake := &fakeMigrateVaults{}
	useMigrateVaults(t, fake)

	err := runMigrate(false)
	if err != nil {
		t.Fatalf("runMigrate() error = %v, want nil", err)
	}

	mustExist(t, filepath.Join(config.DataHome(), migrateVaultTestBloc, "vault", "data", "vault.db"))
	mustExist(t, filepath.Join(config.StateHome(), migrateVaultTestBloc, "logs", "vault", "vault-inception.log"))

	if len(fake.probed) != 1 || fake.probed[0] != migrateVaultTestPort() {
		t.Errorf("probed ports = %v, want [%s]", fake.probed, migrateVaultTestPort())
	}

	if len(fake.asked) != 1 || fake.asked[0] != migrateVaultTestBloc+"-inception-vault" {
		t.Errorf("asked sessions = %v, want [%s-inception-vault]", fake.asked, migrateVaultTestBloc)
	}
}

// Only bloc directories are probed: keys/ and the other known entries are
// not blocs and have no vault.
func TestRunMigrate_ProbesOnlyBlocDirectories(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")

	writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: test\n")
	writeMigrateTestFile(t, filepath.Join(legacyDir, "keys", "k", "id_rsa"), "KEY\n")
	writeMigrateTestFile(t, filepath.Join(legacyDir, "logs", "bootstrap", "x.log"), "{}\n")

	fake := &fakeMigrateVaults{}
	useMigrateVaults(t, fake)

	err := runMigrate(false)
	if err != nil {
		t.Fatalf("runMigrate() error = %v, want nil", err)
	}

	if len(fake.probed) != 0 || len(fake.asked) != 0 {
		t.Errorf("probed %v and asked %v, want nothing probed", fake.probed, fake.asked)
	}
}

// A data directory with no vault/ cannot hold a running inception vault,
// so whatever answers on its hashed port is not a reason to refuse.
func TestRunMigrate_SkipsDataDirectoriesWithoutVault(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")

	writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: test\n")
	writeMigrateTestFile(t, filepath.Join(legacyDir, migrateVaultTestBloc, "ssh", "id_rsa"), "KEY\n")

	port := migrateVaultTestPort()
	fake := &fakeMigrateVaults{
		ports:    map[string]vaultProbeState{port: vaultProbeStranger},
		sessions: map[string]bool{migrateVaultTestBloc + "-inception-vault": true},
	}
	useMigrateVaults(t, fake)

	err := runMigrate(false)
	if err != nil {
		t.Fatalf("runMigrate() error = %v, want nil", err)
	}

	if len(fake.probed) != 0 || len(fake.asked) != 0 {
		t.Errorf("probed %v and asked %v, want nothing probed", fake.probed, fake.asked)
	}

	mustExist(t, filepath.Join(config.DataHome(), migrateVaultTestBloc, "ssh", "id_rsa"))
}

// The probes use the same port and session as the vault commands, so the
// check looks where the bloc's vault actually runs.
func TestBlocInceptionEndpoint_MatchesVaultPaths(t *testing.T) {
	isolateXDGEnv(t)

	got := blocInceptionEndpoint(migrateVaultTestBloc)
	paths := mustInceptionPaths(t, migrateVaultTestBloc, false)

	for _, key := range []string{"port", "tmuxSession"} {
		if got[key] != paths[key] {
			t.Errorf("blocInceptionEndpoint()[%q] = %q, want %q", key, got[key], paths[key])
		}
	}
}

// A bloc whose vault is split across both directories, with keys on one
// side and data on the other, is never merged into one vault/, because the
// merge hides which side held what. The command refuses, names both, and
// moves nothing. A vault/ that is empty on one side holds no vault.
func TestRunMigrate_BlocVaultOnBothSidesRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		xdgEntry string
		refuses  bool
	}{
		"data in the XDG directory":        {xdgEntry: filepath.Join("data", "raft", "raft.db"), refuses: true},
		"empty vault in the XDG directory": {},
	} {
		t.Run(name, func(t *testing.T) {
			home := isolateXDGEnv(t)
			legacyDir := filepath.Join(home, ".ocfp")
			legacyVault := filepath.Join(legacyDir, migrateVaultTestBloc, "vault")
			xdgVault := filepath.Join(config.DataHome(), migrateVaultTestBloc, "vault")

			writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: test\n")
			writeMigrateTestFile(t, filepath.Join(legacyVault, "root.key"), "root")
			writeMigrateTestFile(t, filepath.Join(legacyVault, "unseal.keys"), "unseal")

			if tc.xdgEntry != "" {
				writeMigrateTestFile(t, filepath.Join(xdgVault, tc.xdgEntry), "raft")
			} else if err := os.MkdirAll(xdgVault, 0o700); err != nil {
				t.Fatal(err)
			}

			useMigrateVaults(t, &fakeMigrateVaults{})

			err := runMigrate(false)
			if !tc.refuses {
				if err != nil {
					t.Fatalf("runMigrate() error = %v, want nil", err)
				}

				mustExist(t, filepath.Join(xdgVault, "root.key"))

				return
			}

			if !errors.Is(err, ErrMigrateHasConflicts) {
				t.Fatalf("runMigrate() error = %v, want %v", err, ErrMigrateHasConflicts)
			}

			for _, dir := range []string{legacyVault, xdgVault} {
				if !strings.Contains(err.Error(), dir) {
					t.Errorf("error %q does not name %s", err, dir)
				}
			}

			mustExist(t, filepath.Join(legacyVault, "root.key"))
			mustNotExist(t, filepath.Join(xdgVault, "root.key"))
		})
	}
}

// A vault/ that is a link to the other side's vault/ is refused like any
// bloc with something in both places, and nothing moves.
func TestRunMigrate_LinkedBlocVaultRefuses(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")
	legacyVault := filepath.Join(legacyDir, migrateVaultTestBloc, "vault")
	xdgVault := filepath.Join(config.DataHome(), migrateVaultTestBloc, "vault")

	writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: test\n")
	writeMigrateTestFile(t, filepath.Join(xdgVault, "root.key"), "root")

	if err := os.MkdirAll(filepath.Dir(legacyVault), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.Symlink(xdgVault, legacyVault); err != nil {
		t.Fatal(err)
	}

	useMigrateVaults(t, &fakeMigrateVaults{})

	err := runMigrate(false)
	if !errors.Is(err, ErrMigrateHasConflicts) {
		t.Fatalf("runMigrate() error = %v, want %v", err, ErrMigrateHasConflicts)
	}

	mustExist(t, filepath.Join(xdgVault, "root.key"))
	mustExist(t, filepath.Join(legacyDir, "config.yml"))
}
