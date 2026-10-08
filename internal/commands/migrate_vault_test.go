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
//
// onProbe, when set, runs before each probe answers, with the number of
// probes made so far, counting this one, so a test can act at the moment
// config migrate probes the vaults again under the blocs' locks.
type fakeMigrateVaults struct {
	mu       sync.Mutex
	ports    map[string]vaultProbeState
	sessions map[string]bool
	probed   []string
	asked    []string
	onProbe  func(call int)
}

func (f *fakeMigrateVaults) probes() inceptionLivenessProbes {
	return inceptionLivenessProbes{
		probe: func(_ context.Context, addr string) vaultProbe {
			f.mu.Lock()
			port := addr[strings.LastIndex(addr, ":")+1:]
			f.probed = append(f.probed, port)
			call := len(f.probed)
			hook := f.onProbe
			f.mu.Unlock()

			if hook != nil {
				hook(call)
			}

			f.mu.Lock()
			defer f.mu.Unlock()

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

	// The vaults are probed once before the blocs' locks are taken, and once
	// more while they are held.
	port, session := migrateVaultTestPort(), migrateVaultTestBloc+"-inception-vault"
	if strings.Join(fake.probed, " ") != port+" "+port {
		t.Errorf("probed ports = %v, want [%s %s]", fake.probed, port, port)
	}

	if strings.Join(fake.asked, " ") != session+" "+session {
		t.Errorf("asked sessions = %v, want [%s %s]", fake.asked, session, session)
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

// shortenMigrateBlocLockWait makes config migrate give up on a held bloc lock
// quickly for the rest of the test.
func shortenMigrateBlocLockWait(t *testing.T) {
	t.Helper()

	saved := migrateBlocLockTimeout
	migrateBlocLockTimeout = 100 * time.Millisecond

	t.Cleanup(func() { migrateBlocLockTimeout = saved })
}

// requireLegacyBlocNotMoved checks that the bloc and config.yml are still in
// the legacy directory and nothing of them reached the XDG directories. The
// lock file that config migrate itself takes may exist.
func requireLegacyBlocNotMoved(t *testing.T, legacyDir string) {
	t.Helper()

	blocDir := filepath.Join(legacyDir, migrateVaultTestBloc)
	for _, rel := range []string{"vault/data/vault.db", "vault/root.key", "vault/unseal.keys", "logs/vault/vault-inception.log"} {
		mustExist(t, filepath.Join(blocDir, rel))
	}

	mustExist(t, filepath.Join(legacyDir, "config.yml"))
	mustNotExist(t, filepath.Join(config.DataHome(), migrateVaultTestBloc))
	mustNotExist(t, filepath.Join(config.StateHome(), migrateVaultTestBloc, "logs"))
	mustNotExist(t, filepath.Join(config.ConfigHome(), "config.yml"))
}

// A vault command that holds a bloc's lock is working on that bloc's vault,
// and moving the bloc would pull its directory out from under it, so config
// migrate refuses, names the bloc, and moves nothing. A dry run takes no
// lock, so it still runs.
func TestRunMigrate_RefusesWhileAVaultCommandHoldsABlocLock(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")

	writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: test\n")
	writeLegacyBlocVault(t, legacyDir)
	useMigrateVaults(t, &fakeMigrateVaults{})
	shortenMigrateBlocLockWait(t)

	lockFile := inceptionLockPath(migrateVaultTestBloc, false)

	err := config.WithFileLock(lockFile, time.Second, func() error {
		if dryErr := runMigrate(true); dryErr != nil {
			t.Errorf("runMigrate(dry run) error = %v, want nil", dryErr)
		}

		return runMigrate(false)
	})
	if !errors.Is(err, ErrMigrateInceptionVaultBusy) {
		t.Fatalf("runMigrate() error = %v, want ErrMigrateInceptionVaultBusy", err)
	}

	for _, want := range []string{"bloc " + migrateVaultTestBloc, lockFile, "Nothing was moved"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	requireLegacyBlocNotMoved(t, legacyDir)
}

// A bloc with no vault/ in the legacy directory can still gain one while
// config migrate runs, because a vault command that resolves the legacy
// directory starts its vault there. So config migrate locks every bloc it
// moves, and when a vault command holds the lock of a bloc that has no
// vault/, it refuses and moves nothing.
func TestRunMigrate_RefusesWhileAVaultCommandHoldsTheLockOfABlocWithoutVault(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")
	sshKey := filepath.Join(legacyDir, migrateVaultTestBloc, "ssh", "id_rsa")

	writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: test\n")
	writeMigrateTestFile(t, sshKey, "KEY\n")
	useMigrateVaults(t, &fakeMigrateVaults{})
	shortenMigrateBlocLockWait(t)

	lockFile := inceptionLockPath(migrateVaultTestBloc, false)

	err := config.WithFileLock(lockFile, time.Second, func() error {
		return runMigrate(false)
	})
	if !errors.Is(err, ErrMigrateInceptionVaultBusy) {
		t.Fatalf("runMigrate() error = %v, want ErrMigrateInceptionVaultBusy", err)
	}

	for _, want := range []string{"bloc " + migrateVaultTestBloc, lockFile, "Nothing was moved"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	mustExist(t, sshKey)
	mustExist(t, filepath.Join(legacyDir, "config.yml"))
	mustNotExist(t, filepath.Join(config.DataHome(), migrateVaultTestBloc))
	mustNotExist(t, filepath.Join(config.ConfigHome(), "config.yml"))
}

// A lock that cannot be taken for any reason other than another run holding
// it, such as a lock path that is a directory, is reported as that failure
// and never as a busy bloc, because there is no other run to wait for.
// Nothing moves.
func TestRunMigrate_ReportsALockFailureThatIsNotContention(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")

	writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: test\n")
	writeLegacyBlocVault(t, legacyDir)
	useMigrateVaults(t, &fakeMigrateVaults{})
	shortenMigrateBlocLockWait(t)

	lockFile := inceptionLockPath(migrateVaultTestBloc, false)
	if err := os.MkdirAll(lockFile, 0o700); err != nil {
		t.Fatal(err)
	}

	err := runMigrate(false)
	if err == nil {
		t.Fatal("runMigrate() error = nil, want the failure to take the lock")
	}

	if errors.Is(err, ErrMigrateInceptionVaultBusy) || errors.Is(err, config.ErrFileLockTimeout) {
		t.Errorf("runMigrate() error = %v, want a lock failure that is not reported as a busy bloc", err)
	}

	for _, want := range []string{"bloc " + migrateVaultTestBloc, lockFile, "Nothing was moved"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	if strings.Contains(err.Error(), "Another ocfp run") {
		t.Errorf("error %q blames another ocfp run", err)
	}

	requireLegacyBlocNotMoved(t, legacyDir)
}

// An ocfp command that starts after the live-process guard has run, but
// before config migrate holds the blocs' locks, would have its state and
// logs moved out from under it. So the guard runs again under the locks,
// and a live command found then is refused like one found the first time.
func TestRunMigrate_ChecksForLiveProcessesAgainUnderTheLocks(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")

	writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: test\n")
	writeLegacyBlocVault(t, legacyDir)
	useMigrateVaults(t, &fakeMigrateVaults{})

	saved := migrateBlocLockTimeout
	migrateBlocLockTimeout = 30 * time.Second

	t.Cleanup(func() { migrateBlocLockTimeout = saved })

	// The first guard prunes this stale lock, and it scans the state home
	// after the legacy directory, so once the lock is gone, that guard has
	// read the legacy directory's locks for the last time.
	writeMigrateLockFile(t, config.StateHome(), deadPID(t), time.Now())

	stale, err := filepath.Glob(filepath.Join(config.StateHome(), ".active", "*.lock"))
	if err != nil || len(stale) != 1 {
		t.Fatalf("stale lock setup: %v, %v", stale, err)
	}

	// The live command's lock is written aside now and moved into place
	// only after the first guard has run.
	staging := t.TempDir()
	writeMigrateLockFile(t, staging, liveChildPID(t), time.Now())

	live, err := filepath.Glob(filepath.Join(staging, ".active", "*.lock"))
	if err != nil || len(live) != 1 {
		t.Fatalf("live lock setup: %v, %v", live, err)
	}

	// A vault command holds the bloc's lock, so config migrate waits for it
	// after the first guard, and the live command starts during that wait.
	held, release := make(chan struct{}), make(chan struct{})
	lockDone := make(chan error, 1)

	go func() {
		lockDone <- config.WithFileLock(inceptionLockPath(migrateVaultTestBloc, false), time.Second, func() error {
			close(held)
			<-release

			return nil
		})
	}()
	<-held

	go func() {
		defer close(release)

		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if _, statErr := os.Lstat(stale[0]); statErr == nil {
				continue
			}

			moveErr := os.MkdirAll(filepath.Join(legacyDir, ".active"), 0o700)
			if moveErr == nil {
				moveErr = os.Rename(live[0], filepath.Join(legacyDir, ".active", filepath.Base(live[0])))
			}

			if moveErr != nil {
				t.Errorf("moving the live command's lock into place: %v", moveErr)
			}

			return
		}

		t.Error("the first live-process guard never pruned the stale lock")
	}()

	err = runMigrate(false)
	if !errors.Is(err, ErrMigrateLiveProcessActive) {
		t.Fatalf("runMigrate() error = %v, want ErrMigrateLiveProcessActive", err)
	}

	if lockErr := <-lockDone; lockErr != nil {
		t.Fatalf("holding the bloc's lock: %v", lockErr)
	}

	requireLegacyBlocNotMoved(t, legacyDir)
}

// The vaults are probed again once the blocs' locks are held, because a
// vault command may have started one between the first probe and the locks.
// A vault found then is refused like one found the first time.
func TestRunMigrate_ProbesTheVaultsAgainUnderTheLocks(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")

	writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: test\n")
	writeLegacyBlocVault(t, legacyDir)

	fake := &fakeMigrateVaults{ports: map[string]vaultProbeState{}}
	fake.onProbe = func(call int) {
		if call == 2 {
			fake.mu.Lock()
			fake.ports[migrateVaultTestPort()] = vaultProbeVault
			fake.mu.Unlock()
		}
	}
	useMigrateVaults(t, fake)

	err := runMigrate(false)
	if !errors.Is(err, ErrMigrateInceptionVaultRunning) {
		t.Fatalf("runMigrate() error = %v, want ErrMigrateInceptionVaultRunning", err)
	}

	requireLegacyBlocNotMoved(t, legacyDir)
}

// config migrate holds every bloc's lock, taken in name order, from the
// second probe until the moves end, so a vault command that starts during
// the moves waits for them and then works on the directory the bloc moved
// to.
func TestRunMigrate_VaultCommandStartedDuringTheMovesWaitsForThem(t *testing.T) {
	home := isolateXDGEnv(t)
	legacyDir := filepath.Join(home, ".ocfp")
	other := "ocfp-lab-another"

	writeMigrateTestFile(t, filepath.Join(legacyDir, "config.yml"), "name: test\n")
	writeLegacyBlocVault(t, legacyDir)
	writeMigrateTestFile(t, filepath.Join(legacyDir, other, "vault", "root.key"), "root")

	saved := inceptionLockTimeout
	inceptionLockTimeout = 30 * time.Second

	t.Cleanup(func() { inceptionLockTimeout = saved })

	resolved := make(chan string, 1)
	fake := &fakeMigrateVaults{}
	fake.onProbe = func(call int) {
		// The first two probes come before the locks, one for each bloc.
		if call != 3 {
			return
		}

		for _, bloc := range []string{migrateVaultTestBloc, other} {
			lockErr := config.WithFileLock(inceptionLockPath(bloc, false), 50*time.Millisecond, func() error { return nil })
			if !errors.Is(lockErr, config.ErrFileLockTimeout) {
				t.Errorf("bloc %s's lock was free while config migrate probed under the locks: %v", bloc, lockErr)
			}
		}

		go func() {
			runErr := withLockedInceptionPaths(migrateVaultTestBloc, false, nil, func(paths map[string]string) error {
				resolved <- paths["vaultDir"]

				return nil
			})
			if runErr != nil {
				resolved <- "error: " + runErr.Error()
			}
		}()

		// A vault command that did not wait would resolve the legacy
		// directory in this time, before anything moves.
		time.Sleep(200 * time.Millisecond)
	}
	useMigrateVaults(t, fake)

	err := runMigrate(false)
	if err != nil {
		t.Fatalf("runMigrate() error = %v, want nil", err)
	}

	select {
	case got := <-resolved:
		if want := filepath.Join(config.DataHome(), migrateVaultTestBloc, "vault", "data"); got != want {
			t.Errorf("the vault command resolved %q, want %q", got, want)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the vault command never took the bloc's lock")
	}

	mustNotExist(t, filepath.Join(legacyDir, migrateVaultTestBloc))
}
