package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/fivetwenty-io/ocfp-cli/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// captureInitLog sends what the init components log to an observer.
func captureInitLog(t *testing.T) *observer.ObservedLogs {
	t.Helper()

	core, logs := observer.New(zapcore.InfoLevel)
	logger := zap.New(core).Sugar()

	saved := initLogger
	initLogger = func() *zap.SugaredLogger { return logger }

	t.Cleanup(func() { initLogger = saved })

	return logs
}

// componentFixture is a workstation on which every init component other
// than bastion gets as far as its changes. The bloc has bootstrap state
// with the outputs the BOSH manifest needs, a deployment directory, and a
// DNS name, and fake bosh and cf record their arguments.
type componentFixture struct {
	cfg     *config.Config
	home    string
	sshLog  string
	toolLog string
	roots   []string
}

func newComponentFixture(t *testing.T, bloc string) *componentFixture {
	t.Helper()

	cfg, sshLog, home := connectivityCheckFixture(t, "", 0)
	cfg.DNS = []string{"sys.example.test"}
	t.Setenv("OCFP_BLOC", bloc)

	ocfpHome, stateHome, configHome := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("OCFP_HOME", ocfpHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CONFIG_HOME", configHome)

	toolLog := filepath.Join(t.TempDir(), "tools.log")
	fakeDeployTools(t, toolLog)

	writeBootstrapState(t, cfg.Name, map[string]any{
		"subnet_" + cfg.Name + "-ocfp-0_id":        "subnet-0a1b2c3d",
		"reserved_" + cfg.Name + "-ocfp-0_bosh_ip": "10.0.0.6",
	})
	require.NoError(t, os.MkdirAll(filepath.Join(home, "deployments", cfg.Name), 0o750))

	return &componentFixture{
		cfg: cfg, home: home, sshLog: sshLog, toolLog: toolLog,
		roots: []string{home, ocfpHome, os.Getenv("XDG_DATA_HOME"), stateHome, configHome},
	}
}

// writeBootstrapState writes the bloc's bootstrap state with outputs.
func writeBootstrapState(t *testing.T, bloc string, outputs map[string]any) {
	t.Helper()

	dir, err := state.GetStateDir(bloc)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o750))

	data, err := json.Marshal(map[string]any{"version": "1.0", "blocName": bloc, "outputs": outputs})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, bloc+".json"), data, 0o600))
}

// toolCalls returns the bosh and cf command lines the fake tools recorded.
func (fx *componentFixture) toolCalls(t *testing.T) []string {
	t.Helper()

	data, err := os.ReadFile(fx.toolLog)
	if os.IsNotExist(err) {
		return nil
	}

	require.NoError(t, err)

	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// deploymentDir is the bloc's workstation deployment directory.
func (fx *componentFixture) deploymentDir() string {
	return filepath.Join(fx.home, "deployments", fx.cfg.Name)
}

// runComponentInit runs init for component in a dry or real run.
func (fx *componentFixture) runComponentInit(t *testing.T, component string, dryRun bool) error {
	t.Helper()

	f := &initFlags{dryRun: dryRun}

	return f.executeInitialization(context.Background(), makeCmdNoBloc(t), fx.cfg, component)
}

// dryRunLines returns the messages of the DRY RUN lines a run logged.
func dryRunLines(logs *observer.ObservedLogs) []string {
	var lines []string

	for _, entry := range logs.FilterMessageSnippet("DRY RUN: ").All() {
		lines = append(lines, entry.Message)
	}

	return lines
}

// componentCase is one init component with its bloc, the DRY RUN lines its
// dry run logs, and what its real run does on the fixture.
type componentCase struct {
	component string
	bloc      string
	would     []string
	realTools []string
}

// The lines a dry run logs for the checks it leaves to the real run's
// writes.
const (
	uncheckedLockLine = "DRY RUN: Did not take the lock on the CLI state file or check that it can be written, " +
		"so a real run can still fail when it makes the bloc current"
	uncheckedDirLine = "DRY RUN: Did not check that this directory can be created and written, " +
		"so a real run can still fail when it writes there"
)

var componentCases = []componentCase{
	{"aws", "ocfp-aws-dry", []string{
		uncheckedLockLine,
		"DRY RUN: Would make ocfp-aws-dry the current bloc",
		uncheckedDirLine,
		"DRY RUN: Would write the mgmt Genesis env file",
		"DRY RUN: Would write the ocf Genesis env file",
		"DRY RUN: Would write the prometheus monitoring Genesis env file",
	}, nil},
	{"gcp", "ocfp-gcp-dry", []string{
		uncheckedDirLine,
		"DRY RUN: Would write the mgmt Genesis env file",
		"DRY RUN: Would write the ocf Genesis env file",
		"DRY RUN: Would write the prometheus monitoring Genesis env file",
		uncheckedLockLine,
		"DRY RUN: Would make ocfp-gcp-dry the current bloc",
	}, nil},
	{"pve", "ocfp-pve-dc1", []string{
		uncheckedLockLine,
		"DRY RUN: Would make ocfp-pve-dc1 the current bloc",
		uncheckedDirLine,
		"DRY RUN: Would write the mgmt Genesis env file",
		"DRY RUN: Would write the ocf Genesis env file",
		"DRY RUN: Would write the prometheus monitoring Genesis env file",
		"DRY RUN: Would write the PVE ops files and runtime config for mgmt",
		"DRY RUN: Would write the PVE ops files and runtime config for ocf",
	}, nil},
	{"pg", "", []string{
		"DRY RUN: Would log the PostgreSQL databases it plans; it creates none of them yet",
	}, nil},
	{"bosh", "", []string{
		uncheckedDirLine,
		"DRY RUN: Would write the BOSH manifest",
		"DRY RUN: Would create the director with bosh create-env",
		"DRY RUN: Would write bosh-env.sh",
	}, []string{"bosh create-env"}},
	{"cf", "", []string{
		"DRY RUN: Would upload the cf-deployment release with bosh",
		"DRY RUN: Would deploy Cloud Foundry with bosh",
		"DRY RUN: Would log in to Cloud Foundry with cf",
		"DRY RUN: Would create the default org with cf",
		"DRY RUN: Would create the development space with cf",
		"DRY RUN: Would target the default org and development space with cf",
	}, []string{"bosh upload-release", "bosh -d cf deploy", "cf login", "cf create-org", "cf create-space", "cf target"}},
}

// A dry run of an init component other than bastion makes every change
// the real run makes into a DRY RUN line and changes nothing. The fixture
// lets the real run reach bosh and cf, and write its files, so the dry run
// is held to the point where it would act.
func TestInitDryRunRunsNoComponent(t *testing.T) {
	for _, tc := range componentCases {
		t.Run(tc.component, func(t *testing.T) {
			fx := newComponentFixture(t, tc.bloc)
			logs := captureInitLog(t)
			before := treeOf(t, fx.roots...)

			require.NoError(t, fx.runComponentInit(t, tc.component, true))

			assert.Empty(t, fx.toolCalls(t), "a dry run must not run bosh or cf")
			assert.Equal(t, before, treeOf(t, fx.roots...), "a dry run must not write anything")
			assert.Equal(t, tc.would, dryRunLines(logs))
			assert.Zero(t, logs.FilterMessage("Creating database").Len(), "a dry run lists no database as created")
			assert.Equal(t, 1,
				logs.FilterMessage("Dry run of init "+tc.component+" finished without changing anything").Len())
		})
	}
}

// Only a write finds out whether its directory can be created and written,
// and only SetCurrentBloc takes the lock on the CLI state file. A dry run
// makes neither, so it names the directory and the state file it didn't
// check, once each.
func TestInitDryRunNamesWhatItDidNotCheck(t *testing.T) {
	cases := []struct {
		component, bloc string
		lock            bool
	}{
		{"aws", "ocfp-aws-dry", true},
		{"gcp", "ocfp-gcp-dry", true},
		{"pve", "ocfp-pve-dc1", true},
		{"bosh", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.component, func(t *testing.T) {
			fx := newComponentFixture(t, tc.bloc)
			logs := captureInitLog(t)

			require.NoError(t, fx.runComponentInit(t, tc.component, true))

			want := fx.deploymentDir()
			if tc.bloc != "" {
				dir, err := config.OcfpBlocDir(tc.bloc)
				require.NoError(t, err)

				want = dir
			}

			dirs := logs.FilterMessage(uncheckedDirLine).All()
			require.Len(t, dirs, 1)
			assert.Equal(t, want, dirs[0].ContextMap()["dir"])

			locks := logs.FilterMessage(uncheckedLockLine).All()
			if !tc.lock {
				assert.Empty(t, locks)

				return
			}

			require.Len(t, locks, 1)
			assert.Equal(t, config.StateFilePath(), locks[0].ContextMap()["path"])
		})
	}
}

// The same fixture takes a real run to the changes the dry run skips, so
// the dry-run test above would catch a dry run that made them.
func TestInitRealRunReachesEveryChange(t *testing.T) {
	for _, tc := range componentCases {
		t.Run(tc.component, func(t *testing.T) {
			fx := newComponentFixture(t, tc.bloc)
			logs := captureInitLog(t)
			before := treeOf(t, fx.roots...)

			require.NoError(t, fx.runComponentInit(t, tc.component, false))

			calls := fx.toolCalls(t)
			require.Len(t, calls, len(tc.realTools))

			for i, prefix := range tc.realTools {
				assert.True(t, strings.HasPrefix(calls[i], prefix), "call %d is %q, want %q", i, calls[i], prefix)
			}

			assert.Empty(t, dryRunLines(logs))

			if tc.component != "pg" && tc.component != "cf" {
				assert.NotEqual(t, before, treeOf(t, fx.roots...), "a real run writes its files")
			}
		})
	}
}

// A dry run of init all runs the bastion's dry run, then every other
// component's, and closes with its own line.
func TestInitAllDryRunPreviewsEachComponent(t *testing.T) {
	fx := newComponentFixture(t, "")
	logs := captureInitLog(t)
	bastionCalls := stubBastionInit(t)
	before := treeOf(t, fx.roots...)

	require.NoError(t, fx.runComponentInit(t, KeywordAll, true))

	assert.Equal(t, []bool{true}, *bastionCalls)
	assert.Empty(t, fx.toolCalls(t))
	assert.Equal(t, before, treeOf(t, fx.roots...))

	var want []string
	for _, tc := range componentCases {
		if tc.component == "pg" || tc.component == "bosh" || tc.component == "cf" {
			want = append(want, tc.would...)
		}
	}

	assert.Equal(t, want, dryRunLines(logs))
}

// precondition is one thing a real run refuses, set up on the fixture.
type precondition struct {
	name      string
	component string
	bloc      string
	setup     func(t *testing.T, fx *componentFixture)
	wantErr   string
}

var preconditions = []precondition{
	{"gcp env already exists", "gcp", "ocfp-gcp-dry", func(t *testing.T, fx *componentFixture) {
		t.Helper()
		writeFixtureFile(t, filepath.Join(config.DataHome(), "ocfp-gcp-dry", "deployments", "ocf", "ocfp-gcp-dry-ocf.yml"), "x")
	}, "GCP environment already exists"},
	{"aws vault in both dirs", "aws", "ocfp-aws-dry", vaultInBothDirs("ocfp-aws-dry"), "has a vault in"},
	{"gcp vault in both dirs", "gcp", "ocfp-gcp-dry", vaultInBothDirs("ocfp-gcp-dry"), "has a vault in"},
	{"pve vault in both dirs", "pve", "ocfp-pve-dc1", vaultInBothDirs("ocfp-pve-dc1"), "has a vault in"},
	{"aws unreadable CLI state", "aws", "ocfp-aws-dry", brokenCLIState, "failed to load state"},
	{"gcp unreadable CLI state", "gcp", "ocfp-gcp-dry", brokenCLIState, "failed to load state"},
	{"pve unreadable CLI state", "pve", "ocfp-pve-dc1", brokenCLIState, "failed to load state"},
	{"bosh missing bootstrap state", "bosh", "", func(t *testing.T, fx *componentFixture) {
		t.Helper()
		writeBootstrapState(t, fx.cfg.Name, map[string]any{})
	}, "bootstrap"},
	{"bosh without a state dir", "bosh", "", func(t *testing.T, fx *componentFixture) {
		t.Helper()
		dir, err := state.GetStateDir(fx.cfg.Name)
		require.NoError(t, err)
		require.NoError(t, os.RemoveAll(dir))
	}, "failed to create BOSH manifest"},
	{"bosh state dir is a file", "bosh", "", func(t *testing.T, fx *componentFixture) {
		t.Helper()
		dir, err := state.GetStateDir(fx.cfg.Name)
		require.NoError(t, err)
		require.NoError(t, os.RemoveAll(dir))
		writeFixtureFile(t, dir, "x")
	}, "failed to create state manager: failed to create state directory"},
	{"bosh unreadable bootstrap state", "bosh", "", func(t *testing.T, fx *componentFixture) {
		t.Helper()
		dir, err := state.GetStateDir(fx.cfg.Name)
		require.NoError(t, err)
		writeFixtureFile(t, filepath.Join(dir, fx.cfg.Name+".json"), ":\n- [")
	}, "failed to load bootstrap state"},
	{"bosh pin for the wrong stemcell", "bosh", "", wrongStemcellPin("bosh"), "targets stemcell"},
	{"cf pin for the wrong stemcell", "cf", "", wrongStemcellPin("cf"), "targets stemcell"},
	{"bosh missing deployment dir", "bosh", "", func(t *testing.T, fx *componentFixture) {
		t.Helper()
		require.NoError(t, os.RemoveAll(fx.deploymentDir()))
	}, "failed to write manifest file"},
	{"bosh deployment dir is a file", "bosh", "", func(t *testing.T, fx *componentFixture) {
		t.Helper()
		require.NoError(t, os.RemoveAll(fx.deploymentDir()))
		writeFixtureFile(t, fx.deploymentDir(), "x")
	}, "not a directory"},
	{"cf missing deployment dir", "cf", "", func(t *testing.T, fx *componentFixture) {
		t.Helper()
		require.NoError(t, os.RemoveAll(fx.deploymentDir()))
	}, "CF deployment failed: chdir"},
	{"cf without a DNS name", "cf", "", func(t *testing.T, fx *componentFixture) {
		t.Helper()
		fx.cfg.DNS = nil
	}, "invalid DNS name"},
	{"cf with a bad DNS name", "cf", "", func(t *testing.T, fx *componentFixture) {
		t.Helper()
		fx.cfg.DNS = []string{"bad name!"}
	}, "invalid DNS name"},
	{"bosh not on PATH", "bosh", "", onlyOnPath(), `BOSH deployment failed: exec: "bosh": executable file not found in $PATH`},
	{"cf without bosh on PATH", "cf", "", onlyOnPath("cf"), `CF deployment failed: exec: "bosh": executable file not found in $PATH`},
	{"cf not on PATH", "cf", "", onlyOnPath("bosh"), `CF login failed: exec: "cf": executable file not found in $PATH`},
	{"all stops at bosh without bootstrap state", KeywordAll, "", func(t *testing.T, fx *componentFixture) {
		t.Helper()
		writeBootstrapState(t, fx.cfg.Name, map[string]any{})
	}, "BOSH initialization failed"},
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

// onlyOnPath leaves on PATH the fakes of tools alone, so every other deploy
// tool is missing, as on a workstation that lacks it.
func onlyOnPath(tools ...string) func(t *testing.T, fx *componentFixture) {
	return func(t *testing.T, fx *componentFixture) {
		t.Helper()

		bin := t.TempDir()
		writeFakeDeployTools(t, bin, fx.toolLog, tools...)
		t.Setenv("PATH", bin)
	}
}

// vaultInBothDirs gives bloc a vault in both its XDG and legacy data dirs.
func vaultInBothDirs(bloc string) func(t *testing.T, fx *componentFixture) {
	return func(t *testing.T, _ *componentFixture) {
		t.Helper()
		// With OCFP_HOME unset, the legacy dir is ~/.ocfp and the XDG one
		// is under XDG_DATA_HOME, so the bloc has two dirs. HOME is a temp
		// dir here, so the guard against writing to the real ~/.ocfp can
		// stand down.
		t.Setenv("OCFP_HOME", "")
		t.Setenv("OCFP_TEST_SAFETY_GUARD", "")
		writeFixtureFile(t, filepath.Join(config.DataHome(), bloc, "vault", "data", "raft.db"), "x")
		writeFixtureFile(t, filepath.Join(config.OcfpHome(), bloc, "vault", "data", "raft.db"), "x")
	}
}

// brokenCLIState leaves a CLI state file that can't be parsed.
func brokenCLIState(t *testing.T, _ *componentFixture) {
	t.Helper()
	writeFixtureFile(t, config.StateFilePath(), ":\n- [")
}

// wrongStemcellPin writes a compiled-release pin for a stemcell other than
// the one deploys expect.
func wrongStemcellPin(deployment string) func(t *testing.T, fx *componentFixture) {
	return func(t *testing.T, fx *componentFixture) {
		t.Helper()
		writeFixtureFile(t, filepath.Join(fx.deploymentDir(), "manifests", deployment, "compiled-releases.yml"),
			"# stemcell: ubuntu-jammy/1.0\n")
	}
}

// A dry run refuses wherever the real run refuses, with the same error,
// and it still changes nothing. Each precondition is one the real run
// checks without changing anything, so the dry run makes the same check.
// Both runs use one fixture, the dry run first, so the paths in the two
// errors match.
func TestInitDryRunRefusesWhereTheRealRunRefuses(t *testing.T) {
	for _, tc := range preconditions {
		t.Run(tc.name, func(t *testing.T) {
			fx := newComponentFixture(t, tc.bloc)
			stubBastionInit(t)
			tc.setup(t, fx)
			before := treeOf(t, fx.roots...)

			dryErr := fx.runComponentInit(t, tc.component, true)
			require.Error(t, dryErr, "the dry run must refuse")
			assert.Empty(t, fx.toolCalls(t))
			assert.Equal(t, before, treeOf(t, fx.roots...))

			realErr := fx.runComponentInit(t, tc.component, false)
			require.Error(t, realErr, "the real run must refuse")
			assert.Contains(t, realErr.Error(), tc.wantErr)
			assert.Equal(t, realErr.Error(), dryErr.Error())
		})
	}
}
