package bastion

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/fivetwenty-io/ocfp-cli/internal/output"
	"github.com/fivetwenty-io/ocfp-cli/internal/testenv"
	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

// stepRefusal breaks one thing on the workstation that an init step checks
// before it reaches the bastion, so the real run refuses at that step.
type stepRefusal struct {
	name  string
	step  string
	setup func(t *testing.T, cfg *config.Config)
	is    error
}

var stepRefusals = []stepRefusal{
	{
		name: "no ocfp config",
		step: "config_files",
		setup: func(t *testing.T, _ *config.Config) {
			t.Helper()
			require.NoError(t, os.Remove(filepath.Join(testenv.OcfpHome(), "config.yml")))
		},
		is: ErrOCFPConfigurationFileNotFound,
	},
	{
		name: "bloc missing from the ocfp config",
		step: "config_files",
		setup: func(t *testing.T, _ *config.Config) {
			t.Helper()

			data, err := yaml.Marshal(&config.ConfigFile{Blocs: map[string]*config.Config{"otherbloc": newBaseConfig("otherbloc", "pve")}})
			require.NoError(t, err)
			writeDryRunFile(t, filepath.Join(testenv.OcfpHome(), "config.yml"), string(data))
		},
		is: ErrBlocNotFoundInConfig,
	},
	{
		name: "unknown ocfp CLI source",
		step: "ocfp_cli_setup",
		setup: func(t *testing.T, _ *config.Config) {
			t.Helper()
			t.Setenv("OCFP_CLI_SOURCE", "nightly")
		},
		is: ErrOCFPCLISourceInvalid,
	},
}

// retryQuickly keeps the retry handler's backoff short, so a step that
// fails with an error it retries doesn't hold up the test.
func retryQuickly(m *Manager) {
	m.errorHandler.baseDelay = time.Millisecond
}

// realStepError runs one step of a real run and returns the error the
// retry handler gives it, which is what the real runners wrap. It fails
// the test if the step reached the bastion, because a refusal the dry run
// is meant to repeat has to come before that.
func realStepError(t *testing.T, cfg *config.Config, step string) error {
	t.Helper()

	client := &unconnectedSSHClient{}
	m, _, _ := dryRunManager(t, cfg, &ProvisioningOptions{}, client)
	retryQuickly(m)

	fn := map[string]func(context.Context) error{
		"config_files":   m.copyConfigFiles,
		"ocfp_cli_setup": m.setupOCFPCLI,
	}[step]
	require.NotNil(t, fn, step)

	ctx := context.Background()
	err := m.errorHandler.ExecuteWithRetry(ctx, step, func() error { return fn(ctx) })
	require.Error(t, err)
	require.Empty(t, client.recorded(), "the real run refuses before it reaches the bastion")

	return err
}

// A dry run of the full step list makes the checks of the steps that read
// only our own files, so it refuses a missing ocfp config, a bloc the
// config doesn't hold, or an unknown ocfp CLI source with the error the
// real run gives. It still reaches nothing on the bastion, and it leaves
// the checkpoints alone when it fails.
func TestDryRunStepsRefuseWhereTheRealRunRefuses(t *testing.T) {
	for _, refusal := range stepRefusals {
		for _, parallel := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/parallel=%t", refusal.name, parallel), func(t *testing.T) {
				cfg := dryRunBloc(t)
				dryRunWorkstation(t, cfg)
				refusal.setup(t, cfg)

				seeded := seedCheckpoints(t, cfg)
				want := fmt.Sprintf("phase %s failed: %v", refusal.step, realStepError(t, cfg, refusal.step))

				client := &unconnectedSSHClient{}
				m, _, _ := dryRunManager(t, cfg, &ProvisioningOptions{DryRun: true, Parallel: parallel}, client)
				retryQuickly(m)

				err := m.syncAndRun(context.Background())
				require.ErrorIs(t, err, refusal.is)
				assert.EqualError(t, err, want)
				assert.Empty(t, client.recorded(), "a dry run must not send anything to the bastion")
				assert.Equal(t, seeded, readCheckpoints(t), "a failed dry run must not change the checkpoints")
			})
		}
	}
}

// The parallel workers make the same checks, and they hand back the
// error unwrapped, as they do in a real run.
func TestDryRunWorkerRefusesWhereTheRealRunRefuses(t *testing.T) {
	for _, refusal := range stepRefusals {
		t.Run(refusal.name, func(t *testing.T) {
			cfg := dryRunBloc(t)
			dryRunWorkstation(t, cfg)
			refusal.setup(t, cfg)

			want := realStepError(t, cfg, refusal.step)

			client := &unconnectedSSHClient{}
			m, _, _ := dryRunManager(t, cfg, &ProvisioningOptions{DryRun: true}, client)
			retryQuickly(m)
			m.progress.TotalSteps = 1

			err := m.runPhasesParallel(context.Background(), []struct {
				name string
				fn   func(context.Context) error
			}{{refusal.step, func(context.Context) error { return nil }}}, 0)
			require.ErrorIs(t, err, refusal.is)
			assert.EqualError(t, err, want.Error())
			assert.Empty(t, client.recorded())
		})
	}
}

// A resumed dry run skips the steps an earlier run finished, and so it
// skips their checks too, as the real run does.
func TestDryRunSkipsTheChecksOfResumedSteps(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		t.Run(fmt.Sprintf("parallel=%t", parallel), func(t *testing.T) {
			cfg := dryRunBloc(t)
			dryRunWorkstation(t, cfg)
			require.NoError(t, os.Remove(filepath.Join(testenv.OcfpHome(), "config.yml")))
			t.Setenv("OCFP_CLI_SOURCE", "nightly")

			client := &unconnectedSSHClient{}
			m, _, _ := dryRunManager(t, cfg, &ProvisioningOptions{DryRun: true, Parallel: parallel, Resume: true}, client)
			m.progress.Checkpoints["config_files"] = true
			m.progress.Checkpoints["ocfp_cli_setup"] = true

			require.NoError(t, m.syncAndRun(context.Background()))
			assert.Empty(t, client.recorded())
		})
	}
}

// On the bastion itself, the local step list makes the ocfp CLI check as
// well, and wraps its error as the local real run does. A dry run that
// passes runs nothing on the host.
func TestLocalExecutorDryRunChecksTheOCFPCLISource(t *testing.T) {
	dryRunWorkstation(t, dryRunBloc(t))

	mock := &ocfpCLIMockSSHClient{}
	le, m := localModeManager(&ProvisioningOptions{DryRun: true}, mock)
	require.NoError(t, le.executeLocalPhases(context.Background(), m))
	assert.Empty(t, mock.commands, "a dry run on the bastion must not run anything")
	assert.Empty(t, mock.transfers)

	t.Setenv("OCFP_CLI_SOURCE", "nightly")

	real := &ocfpCLIMockSSHClient{}
	_, realManager := localModeManager(&ProvisioningOptions{}, real)
	realErr := realManager.setupOCFPCLI(context.Background())
	require.ErrorIs(t, realErr, ErrOCFPCLISourceInvalid)
	assert.Empty(t, real.commands, "the real run refuses before it runs anything")

	mock = &ocfpCLIMockSSHClient{}
	le, m = localModeManager(&ProvisioningOptions{DryRun: true}, mock)
	err := le.executeLocalPhases(context.Background(), m)
	require.ErrorIs(t, err, ErrOCFPCLISourceInvalid)
	assert.EqualError(t, err, fmt.Sprintf("local phase ocfp_cli_setup failed: %v", realErr))
	assert.Empty(t, mock.commands)
}

// When the local ocfp build can't be found, the real run keeps the ocfp
// already on the bastion and fails when there is none. A dry run can't
// look, so it says it didn't, and it never claims to keep one.
func TestDryRunSaysItDidNotLookForTheBastionsOCFP(t *testing.T) {
	cfg := dryRunBloc(t)
	dryRunWorkstation(t, cfg)
	t.Setenv("OCFP_CLI_SOURCE", "local")
	t.Setenv("OCFP_BINARY_PATH", filepath.Join(t.TempDir(), "missing"))

	client := &unconnectedSSHClient{}
	m, _, logs := dryRunManager(t, cfg, &ProvisioningOptions{DryRun: true}, client)

	require.NoError(t, m.syncAndRun(context.Background()))
	assert.Empty(t, client.recorded())
	assert.Equal(t, 1, logs.FilterMessage(
		"DRY RUN: Did not check for an ocfp on the bastion to keep, because that reads the bastion, and a real run fails here without one").Len())
	assert.Zero(t, logs.FilterMessage("Failed to install OCFP binary, keeping the existing one").Len())
}

// A dry run says that it didn't check whether the bastion is already
// provisioned, because a real run that finds it so skips the step list.
func TestDryRunSaysItDidNotCheckTheProvisionedMarker(t *testing.T) {
	cfg := dryRunBloc(t)
	dryRunWorkstation(t, cfg)

	client := &unconnectedSSHClient{}
	m, _, logs := dryRunManager(t, cfg, &ProvisioningOptions{DryRun: true}, client)

	require.NoError(t, m.syncAndRun(context.Background()))
	assert.Empty(t, client.recorded())
	assert.Equal(t, 1, logs.FilterMessage(
		"DRY RUN: Did not check whether the bastion is already provisioned, because that reads the bastion").Len())
}

// A dry run neither waits for the bastion's SSH port nor connects, so it
// never checks the bastion's host key, and it says so rather than letting
// a passing dry run stand for a working connection.
func TestDryRunSaysItDidNotConnect(t *testing.T) {
	cfg := dryRunBloc(t)
	dryRunWorkstation(t, cfg)
	cfg.APIEndpoint = "https://pve.example.invalid:8006"
	cfg.AuthToken = "root@pam!ocfp"
	cfg.TokenSecret = "secret"
	cfg.BastionIP = "10.61.148.10"
	block, err := gossh.MarshalPrivateKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, ed25519.SeedSize)), "")
	require.NoError(t, err)
	writeDryRunFile(t, filepath.Join(config.OcfpSSHKeyDir(cfg.Name), "id_ed25519"), string(pem.EncodeToMemory(block)))

	m, _, logs := dryRunManager(t, cfg, &ProvisioningOptions{DryRun: true}, &unconnectedSSHClient{})

	require.NoError(t, m.setupInfrastructure(context.Background()))
	assert.Equal(t, 1, logs.FilterMessage(
		"DRY RUN: Did not wait for the bastion's SSH port or connect to it, so its host key was not checked").Len())
	_, err = os.Stat(filepath.Join(os.Getenv("HOME"), ".ssh", "known_hosts"))
	assert.True(t, os.IsNotExist(err), "a dry run must not write known_hosts")
}

// When the bloc has no key file, the real run restores the bastion's SSH
// key from the config before it connects. A dry run reads the config's copy
// in its place, and it writes no key file or key directory.
func TestDryRunDoesNotRestoreTheBastionKey(t *testing.T) {
	cfg := dryRunBloc(t)
	dryRunWorkstation(t, cfg)
	cfg.APIEndpoint = "https://pve.example.invalid:8006"
	cfg.AuthToken = "root@pam!ocfp"
	cfg.TokenSecret = "secret"
	cfg.BastionIP = "10.61.148.10"
	block, err := gossh.MarshalPrivateKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{4}, ed25519.SeedSize)), "")
	require.NoError(t, err)
	cfg.Keys = map[string]string{cfg.Name + "-keypair": string(pem.EncodeToMemory(block))}

	keyDir := config.OcfpSSHKeyDir(cfg.Name)
	_, err = os.Stat(keyDir)
	require.True(t, os.IsNotExist(err), "the bloc starts with no key directory")

	m, _, _ := dryRunManager(t, cfg, &ProvisioningOptions{DryRun: true}, &unconnectedSSHClient{})

	require.NoError(t, m.setupInfrastructure(context.Background()))
	_, err = os.Stat(keyDir)
	assert.True(t, os.IsNotExist(err), "a dry run must not restore the key")
}

// A dry run that stops at a step the real run would fail at closes with a
// summary that says the dry run failed and changed nothing. It counts the
// steps before the failed one as previewed and the failed one as failed.
func TestFailedDryRunSummarySaysNothingChanged(t *testing.T) {
	// config_files is the fourth step of the sequential list and the
	// seventh of the parallel one.
	for parallel, previewed := range map[bool]int{false: 3, true: 6} {
		t.Run(fmt.Sprintf("parallel=%t", parallel), func(t *testing.T) {
			cfg := dryRunBloc(t)
			dryRunWorkstation(t, cfg)
			require.NoError(t, os.Remove(filepath.Join(testenv.OcfpHome(), "config.yml")))

			var out bytes.Buffer

			m, _, _ := dryRunManager(t, cfg,
				&ProvisioningOptions{DryRun: true, Parallel: parallel, ProgressOut: &out}, &unconnectedSSHClient{})
			require.ErrorIs(t, m.syncAndRun(context.Background()), ErrOCFPConfigurationFileNotFound)

			text := out.String()
			assert.Contains(t, text, "Status: Dry run failed without changing anything")
			assert.Contains(t, text, fmt.Sprintf("Phases previewed: %d\n", previewed))
			assert.Contains(t, text, "Phases failed: 1\n")
			assert.NotContains(t, text, "Status: Failed")
			assert.NotContains(t, text, "Phases completed")
		})
	}

	t.Run("json", func(t *testing.T) {
		cfg := dryRunBloc(t)
		dryRunWorkstation(t, cfg)
		require.NoError(t, os.Remove(filepath.Join(testenv.OcfpHome(), "config.yml")))

		var out bytes.Buffer

		m, _, _ := dryRunManager(t, cfg, &ProvisioningOptions{DryRun: true}, &unconnectedSSHClient{})
		m.reporter = NewProgressReporter(&out, output.ModeJSON, m.progress)

		require.ErrorIs(t, m.executeInitializationPhases(context.Background()), ErrOCFPConfigurationFileNotFound)

		event := summaryEvent(t, out.String())
		assert.Equal(t, false, event["success"])
		assert.Equal(t, true, event["dry_run"])
		assert.Equal(t, false, event["changed"])
		assert.Equal(t, "Dry run failed without changing anything", event["message"])
		assert.InDelta(t, 0, event["completed_phases"], 0)
		assert.InDelta(t, 3, event["previewed_phases"], 0)
		assert.InDelta(t, 1, event["failed_phases"], 0)
		assert.InDelta(t, len(m.getInitializationPhases()), event["total_phases"], 0)
	})
}

// A resumed dry run skips the steps that the checkpoint marks as done, as
// the real run does. Its summary counts them as skipped, and counts as
// previewed only the steps that this run previewed, whether it finishes or
// stops at a step the real run would fail at.
func TestResumedDryRunSummaryCountsSkippedSteps(t *testing.T) {
	// system_setup and directories come before config_files in both step
	// lists, which is the fourth of the sequential list and the seventh of
	// the parallel one.
	for _, parallel := range []bool{false, true} {
		for _, fails := range []bool{false, true} {
			t.Run(fmt.Sprintf("parallel=%t fails=%t", parallel, fails), func(t *testing.T) {
				cfg := dryRunBloc(t)
				dryRunWorkstation(t, cfg)

				if fails {
					require.NoError(t, os.Remove(filepath.Join(testenv.OcfpHome(), "config.yml")))
				}

				var out bytes.Buffer

				m, _, _ := dryRunManager(t, cfg,
					&ProvisioningOptions{DryRun: true, Resume: true, Parallel: parallel}, &unconnectedSSHClient{})
				m.progress.Checkpoints = map[string]bool{"system_setup": true, "directories": true}
				m.reporter = NewProgressReporter(&out, output.ModeJSON, m.progress)

				err := m.executeInitializationPhases(context.Background())

				total := len(m.getInitializationPhases())
				if parallel {
					total = len(m.parallelPrePhaseList()) + len(m.parallelParPhaseList()) + len(m.parallelPostPhaseList())
				}

				previewed, failed := total-2, 0
				if fails {
					require.ErrorIs(t, err, ErrOCFPConfigurationFileNotFound)

					previewed, failed = 1, 1
					if parallel {
						previewed = 4
					}
				} else {
					require.NoError(t, err)
				}

				event := summaryEvent(t, out.String())
				assert.Equal(t, true, event["dry_run"])
				assert.Equal(t, !fails, event["success"])
				assert.InDelta(t, total, event["total_phases"], 0)
				assert.InDelta(t, previewed, event["previewed_phases"], 0)
				assert.InDelta(t, 2, event["skipped_phases"], 0)
				assert.InDelta(t, failed, event["failed_phases"], 0)
			})
		}
	}

	// binary_tools runs in the parallel batch, so the batch's workers count
	// it as skipped, not the sequential steps around them.
	t.Run("parallel batch", func(t *testing.T) {
		cfg := dryRunBloc(t)
		dryRunWorkstation(t, cfg)

		var out bytes.Buffer

		m, _, _ := dryRunManager(t, cfg,
			&ProvisioningOptions{DryRun: true, Resume: true, Parallel: true}, &unconnectedSSHClient{})
		m.progress.Checkpoints = map[string]bool{"system_setup": true, "directories": true, "binary_tools": true}
		m.reporter = NewProgressReporter(&out, output.ModeJSON, m.progress)

		var batch []string
		for _, p := range m.parallelParPhaseList() {
			batch = append(batch, p.name)
		}

		require.Contains(t, batch, "binary_tools")
		require.NoError(t, m.executeInitializationPhases(context.Background()))

		total := len(m.parallelPrePhaseList()) + len(m.parallelParPhaseList()) + len(m.parallelPostPhaseList())

		event := summaryEvent(t, out.String())
		assert.InDelta(t, 3, event["skipped_phases"], 0)
		assert.InDelta(t, total-3, event["previewed_phases"], 0)
		assert.InDelta(t, 0, event["failed_phases"], 0)
	})
}

// A real run that fails reports no closing summary, as before.
func TestFailedRealRunReportsNoSummary(t *testing.T) {
	cfg := dryRunBloc(t)
	dryRunWorkstation(t, cfg)

	var out bytes.Buffer

	m, _, _ := dryRunManager(t, cfg, &ProvisioningOptions{}, &unconnectedSSHClient{})
	retryQuickly(m)
	m.reporter = NewProgressReporter(&out, output.ModeJSON, m.progress)

	require.Error(t, m.executeInitializationPhases(context.Background()))
	assert.NotContains(t, out.String(), `"summary"`)
	assert.NotContains(t, out.String(), "Dry run")
}
