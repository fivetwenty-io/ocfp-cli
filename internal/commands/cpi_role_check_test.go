package commands

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const cpiRunbookPath = "../../docs/runbooks/pve/02-pve-foundation.md"

// pmxScript is a commandRunner whose answers come from a function, so a
// test can return different output for the same command on a second call. It
// records every call and fails the test if any `role set` lacks --append.
type pmxScript struct {
	t       *testing.T
	calls   [][]string
	lookErr error
	handle  func(args []string) (stdout, stderr []byte, err error)
}

func (s *pmxScript) record(name string, args []string) {
	call := append([]string{name}, args...)
	s.calls = append(s.calls, call)

	joined := " " + strings.Join(args, " ") + " "
	if strings.Contains(joined, " role set ") {
		assert.Contains(s.t, args, "--append", "role set must always append: %v", call)
	}
}

func (s *pmxScript) RunSplit(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	s.record(name, args)

	return s.handle(args)
}

func (s *pmxScript) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, _, err := s.RunSplit(ctx, name, args...)

	return out, err
}

func (s *pmxScript) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, errOut, err := s.RunSplit(ctx, name, args...)

	return append(out, errOut...), err
}

func (s *pmxScript) LookPath(string) error { return s.lookErr }

// roleSetCalls returns every recorded `role set` invocation.
func (s *pmxScript) roleSetCalls() [][]string {
	var sets [][]string

	for _, c := range s.calls {
		if strings.Contains(" "+strings.Join(c, " ")+" ", " role set ") {
			sets = append(sets, c)
		}
	}

	return sets
}

func rolePrivsJSON(privs []string) []byte {
	parts := make([]string, len(privs))
	for i, p := range privs {
		parts[i] = `"` + p + `": true`
	}

	return []byte("{" + strings.Join(parts, ",") + "}")
}

func without(all []string, drop ...string) []string {
	var out []string

	for _, p := range all {
		if !slices.Contains(drop, p) {
			out = append(out, p)
		}
	}

	return out
}

func installPmxScript(t *testing.T, s *pmxScript) {
	t.Helper()

	s.t = t
	orig := runner
	runner = s

	t.Cleanup(func() { runner = orig })
}

const contextShowJSON = `{"name":"lab","host":"pve.example.test","port":8006,"protocol":"https","secret":"***"}`

// roleReads builds a handler that answers context show and serves the given
// role payloads in order, repeating the last one.
func roleReads(reads ...[]byte) func(args []string) ([]byte, []byte, error) {
	i := 0

	return func(args []string) ([]byte, []byte, error) {
		joined := strings.Join(args, " ")

		switch {
		case strings.Contains(joined, "context show"):
			return []byte(contextShowJSON), nil, nil
		case strings.Contains(joined, "role get"):
			out := reads[min(i, len(reads)-1)]
			i++

			return out, nil, nil
		case strings.Contains(joined, "role set"):
			return []byte(`{"message":"updated"}`), nil, nil
		}

		return nil, nil, errors.New("unexpected pmx call: " + joined)
	}
}

func roleGetCount(s *pmxScript) int {
	n := 0

	for _, c := range s.calls {
		if strings.Contains(strings.Join(c, " "), "role get") {
			n++
		}
	}

	return n
}

func TestCPIRolePrivilegesMatchRunbook(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(cpiRunbookPath)
	require.NoError(t, err)

	re := regexp.MustCompile("(?s)pmx pve access role create OCFPCpi --privs\\s*\\\\?\\s*\"(.*?)\"")
	m := re.FindSubmatch(data)
	require.NotNil(t, m, "runbook role create block not found")

	text := strings.NewReplacer("\\\n", "", "\n", "").Replace(string(m[1]))
	fromRunbook := strings.Split(text, ",")

	got := append([]string(nil), cpiRolePrivileges...)
	sort.Strings(got)
	sort.Strings(fromRunbook)

	assert.Equal(t, fromRunbook, got, "Go privilege list and runbook must match exactly")
	assert.Contains(t, cpiRolePrivileges, "Pool.Audit")
}

func TestCPIRoleAllPresent(t *testing.T) {
	s := &pmxScript{handle: roleReads(rolePrivsJSON(cpiRolePrivileges))}
	installPmxScript(t, s)

	var out bytes.Buffer

	err := runCheckCPIRole(context.Background(), "lab", false, &out)
	require.NoError(t, err)

	assert.Contains(t, out.String(), "OCFPCpi")
	assert.Contains(t, out.String(), "lab")
	assert.Contains(t, out.String(), "https://pve.example.test:8006")
	assert.Empty(t, s.roleSetCalls())
}

func TestCPIRoleMissingWithoutApplyReportsAndFails(t *testing.T) {
	s := &pmxScript{handle: roleReads(rolePrivsJSON(without(cpiRolePrivileges, "Pool.Audit", "VM.Snapshot")))}
	installPmxScript(t, s)

	var out bytes.Buffer

	err := runCheckCPIRole(context.Background(), "lab", false, &out)
	require.ErrorIs(t, err, ErrCPIRolePrivsMissing)

	text := out.String()
	assert.Contains(t, text, "Pool.Audit")
	assert.Contains(t, text, "VM.Snapshot")
	assert.Contains(t, text, "pmx -c lab pve access role set OCFPCpi --privs Pool.Audit,VM.Snapshot --append")
	assert.Empty(t, s.roleSetCalls())
}

func TestCPIRoleApplyAppendsOnceAndRereads(t *testing.T) {
	s := &pmxScript{handle: roleReads(
		rolePrivsJSON(without(cpiRolePrivileges, "Pool.Audit", "VM.Snapshot")),
		rolePrivsJSON(cpiRolePrivileges),
	)}
	installPmxScript(t, s)

	var out bytes.Buffer

	err := runCheckCPIRole(context.Background(), "lab", true, &out)
	require.NoError(t, err)

	sets := s.roleSetCalls()
	require.Len(t, sets, 1)
	assert.Equal(t, []string{
		"pmx", "-c", "lab", "pve", "access", "role", "set", "OCFPCpi",
		"--privs", "Pool.Audit,VM.Snapshot", "--append",
	}, sets[0])
	assert.Equal(t, 2, roleGetCount(s))
}

func TestCPIRoleApplyWithNothingMissingDoesNotSet(t *testing.T) {
	s := &pmxScript{handle: roleReads(rolePrivsJSON(cpiRolePrivileges))}
	installPmxScript(t, s)

	err := runCheckCPIRole(context.Background(), "", true, &bytes.Buffer{})
	require.NoError(t, err)
	assert.Empty(t, s.roleSetCalls())
}

func TestCPIRoleApplyStillMissingFails(t *testing.T) {
	s := &pmxScript{handle: roleReads(
		rolePrivsJSON(without(cpiRolePrivileges, "Pool.Audit")),
		rolePrivsJSON(without(cpiRolePrivileges, "Pool.Audit")),
	)}
	installPmxScript(t, s)

	var out bytes.Buffer

	err := runCheckCPIRole(context.Background(), "lab", true, &out)
	require.ErrorIs(t, err, ErrCPIRoleStillMissing)
	assert.Contains(t, out.String(), "Pool.Audit")
	assert.Len(t, s.roleSetCalls(), 1)
}

func TestCPIRoleExtraPrivilegesAreInformational(t *testing.T) {
	extra := append(append([]string(nil), cpiRolePrivileges...), "Sys.PowerMgmt", "Permissions.Modify")
	s := &pmxScript{handle: roleReads(rolePrivsJSON(extra))}
	installPmxScript(t, s)

	var out bytes.Buffer

	require.NoError(t, runCheckCPIRole(context.Background(), "lab", false, &out))
	assert.Contains(t, out.String(), "Permissions.Modify")
	assert.Contains(t, out.String(), "Sys.PowerMgmt")
	assert.Empty(t, s.roleSetCalls())
}

func TestCPIRoleFalseFlagCountsAsMissing(t *testing.T) {
	have := without(cpiRolePrivileges, "Pool.Audit")
	merged := append(rolePrivsJSON(have)[:len(rolePrivsJSON(have))-1], []byte(`,"Pool.Audit":false}`)...)

	s := &pmxScript{handle: roleReads(merged)}
	installPmxScript(t, s)

	err := runCheckCPIRole(context.Background(), "lab", false, &bytes.Buffer{})
	require.ErrorIs(t, err, ErrCPIRolePrivsMissing)
}

func exitErr(t *testing.T, code string) error {
	t.Helper()

	err := exec.Command("sh", "-c", "exit "+code).Run()
	require.Error(t, err)

	return err
}

func TestCPIRoleMissingRoleNamesRunbook(t *testing.T) {
	// PVE answers a missing role with a 500 and this message, which pmx
	// prints on stderr, so the exit code is the generic 1.
	msg := `get role "OCFPCpi": access.GetRoles: role 'OCFPCpi' does not exist (code: 500)`
	s := &pmxScript{handle: func(args []string) ([]byte, []byte, error) {
		if strings.Contains(strings.Join(args, " "), "role get") {
			return nil, []byte(msg), exitErr(t, "1")
		}

		return []byte(contextShowJSON), nil, nil
	}}
	installPmxScript(t, s)

	err := runCheckCPIRole(context.Background(), "lab", true, &bytes.Buffer{})
	require.ErrorIs(t, err, ErrCPIRoleNotFound)
	assert.Contains(t, err.Error(), "02-pve-foundation.md")
	assert.Empty(t, s.roleSetCalls())
}

func TestCPIRoleOtherNotFoundErrorsArePmxFailures(t *testing.T) {
	cases := map[string]struct {
		stdout, stderr string
		code           string
	}{
		"context not found": {stderr: `context "x" not found; available: lab`, code: "1"},
		"http 404":          {stderr: "get role: 404 not found", code: "5"},
		"other role":        {stderr: "role 'Other' does not exist", code: "1"},
		"stdout only":       {stdout: "role 'OCFPCpi' does not exist-ish secret-stdout", stderr: "boom", code: "1"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := &pmxScript{handle: func(args []string) ([]byte, []byte, error) {
				if strings.Contains(strings.Join(args, " "), "role get") {
					return []byte(tc.stdout), []byte(tc.stderr), exitErr(t, tc.code)
				}

				return []byte(contextShowJSON), nil, nil
			}}
			installPmxScript(t, s)

			err := runCheckCPIRole(context.Background(), "x", true, &bytes.Buffer{})
			require.ErrorIs(t, err, ErrPmxFailed)
			assert.NotErrorIs(t, err, ErrCPIRoleNotFound)
			assert.Contains(t, err.Error(), tc.stderr)
			assert.NotContains(t, err.Error(), "secret-stdout")
			assert.Empty(t, s.roleSetCalls())
		})
	}
}

func TestCPIRolePmxFailure(t *testing.T) {
	s := &pmxScript{handle: func(args []string) ([]byte, []byte, error) {
		if strings.Contains(strings.Join(args, " "), "role get") {
			return nil, []byte("connection refused"), exitErr(t, "3")
		}

		return []byte(contextShowJSON), nil, nil
	}}
	installPmxScript(t, s)

	err := runCheckCPIRole(context.Background(), "lab", false, &bytes.Buffer{})
	require.ErrorIs(t, err, ErrPmxFailed)
	assert.Contains(t, err.Error(), "connection refused")
	assert.NotErrorIs(t, err, ErrCPIRoleNotFound)
}

func TestCPIRoleApplyFailureSurfaces(t *testing.T) {
	s := &pmxScript{handle: func(args []string) ([]byte, []byte, error) {
		joined := strings.Join(args, " ")

		switch {
		case strings.Contains(joined, "role set"):
			return nil, []byte("permission denied"), exitErr(t, "4")
		case strings.Contains(joined, "role get"):
			return rolePrivsJSON(without(cpiRolePrivileges, "Pool.Audit")), nil, nil
		}

		return []byte(contextShowJSON), nil, nil
	}}
	installPmxScript(t, s)

	err := runCheckCPIRole(context.Background(), "lab", true, &bytes.Buffer{})
	require.ErrorIs(t, err, ErrPmxFailed)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestCPIRoleBadJSON(t *testing.T) {
	s := &pmxScript{handle: roleReads([]byte("not json"))}
	installPmxScript(t, s)

	err := runCheckCPIRole(context.Background(), "lab", false, &bytes.Buffer{})
	require.ErrorIs(t, err, ErrPmxFailed)
}

func TestCPIRolePmxNotInstalled(t *testing.T) {
	s := &pmxScript{lookErr: errors.New("not found"), handle: roleReads(nil)}
	installPmxScript(t, s)

	err := runCheckCPIRole(context.Background(), "lab", false, &bytes.Buffer{})
	require.ErrorIs(t, err, ErrPmxNotInstalled)
	assert.Empty(t, s.calls)
}

func TestCPIRolePmxContextPassedAsC(t *testing.T) {
	s := &pmxScript{handle: roleReads(rolePrivsJSON(cpiRolePrivileges))}
	installPmxScript(t, s)

	require.NoError(t, runCheckCPIRole(context.Background(), "lab-b", false, &bytes.Buffer{}))

	for _, c := range s.calls {
		require.GreaterOrEqual(t, len(c), 3)
		assert.Equal(t, []string{"pmx", "-c", "lab-b"}, c[:3])
	}
}

func TestCPIRoleNoContextOmitsC(t *testing.T) {
	s := &pmxScript{handle: roleReads(rolePrivsJSON(cpiRolePrivileges))}
	installPmxScript(t, s)

	require.NoError(t, runCheckCPIRole(context.Background(), "", false, &bytes.Buffer{}))

	for _, c := range s.calls {
		assert.NotContains(t, c, "-c")
	}
}

func TestCPIRoleContextShowFailureIsNotFatal(t *testing.T) {
	s := &pmxScript{handle: func(args []string) ([]byte, []byte, error) {
		if strings.Contains(strings.Join(args, " "), "context show") {
			return nil, []byte("boom"), errors.New("exit 1")
		}

		return rolePrivsJSON(cpiRolePrivileges), nil, nil
	}}
	installPmxScript(t, s)

	var out bytes.Buffer

	require.NoError(t, runCheckCPIRole(context.Background(), "", false, &out))
	assert.Contains(t, out.String(), "unknown")
}

func TestCPIRoleSetArgsAlwaysAppend(t *testing.T) {
	t.Parallel()

	for _, ctxName := range []string{"", "lab"} {
		args, err := cpiRoleSetArgs(ctxName, []string{"Pool.Audit"})
		require.NoError(t, err)
		assert.Contains(t, args, "--append")
		assert.Equal(t, "Pool.Audit", args[slices.Index(args, "--privs")+1])
	}
}

func TestCPIRoleSetArgsRefusesEmptyList(t *testing.T) {
	t.Parallel()

	for _, list := range [][]string{nil, {}} {
		args, err := cpiRoleSetArgs("lab", list)
		require.ErrorIs(t, err, ErrNoPrivilegesToSet)
		assert.Nil(t, args)
	}
}

func TestCPIRoleReportsEnvEndpointOverride(t *testing.T) {
	t.Setenv("PMX_API_ENDPOINT", "https://other.example.test:8006")

	s := &pmxScript{handle: roleReads(rolePrivsJSON(cpiRolePrivileges))}
	installPmxScript(t, s)

	var out bytes.Buffer

	require.NoError(t, runCheckCPIRole(context.Background(), "lab", false, &out))
	assert.Contains(t, out.String(), "https://other.example.test:8006")
	assert.Contains(t, out.String(), "PMX_API_ENDPOINT overrides")
	assert.NotContains(t, out.String(), "pve.example.test")
}

func TestCPIRoleNoOverrideWhenEnvEndpointUnset(t *testing.T) {
	t.Setenv("PMX_API_ENDPOINT", "")

	s := &pmxScript{handle: roleReads(rolePrivsJSON(cpiRolePrivileges))}
	installPmxScript(t, s)

	var out bytes.Buffer

	require.NoError(t, runCheckCPIRole(context.Background(), "lab", false, &out))
	assert.NotContains(t, out.String(), "overrides")
}

func TestCPIRolePluralisesPrivileges(t *testing.T) {
	s := &pmxScript{handle: roleReads(rolePrivsJSON(without(cpiRolePrivileges, "Pool.Audit")))}
	installPmxScript(t, s)

	var out bytes.Buffer

	_ = runCheckCPIRole(context.Background(), "lab", false, &out)
	assert.Contains(t, out.String(), "missing 1 privilege:")
	assert.NotContains(t, out.String(), "1 privileges")
}

func TestValidateCheckCPIRoleOptions(t *testing.T) {
	t.Parallel()

	require.NoError(t, validateConfigureModes(&configureOptions{checkCPIRole: true}))
	require.NoError(t, validateConfigureModes(&configureOptions{checkCPIRole: true, apply: true, pmxContext: "lab"}))

	err := validateConfigureModes(&configureOptions{checkCPIRole: true, pruneDuplicates: true})
	require.ErrorIs(t, err, ErrConfigureModeConflict)

	err = validateConfigureModes(&configureOptions{checkCPIRole: true, pruneDuplicates: true, apply: true})
	require.ErrorIs(t, err, ErrConfigureModeConflict)

	require.ErrorIs(t, validateConfigureModes(&configureOptions{apply: true}), ErrApplyWithoutPrune)

	for _, o := range []*configureOptions{
		{checkCPIRole: true, dryRun: true},
		{checkCPIRole: true, skipRoutes: true},
		{checkCPIRole: true, skipFloatingIPs: true},
		{checkCPIRole: true, skipSecGroups: true},
		{checkCPIRole: true, skipBastion: true},
	} {
		require.ErrorIs(t, validateConfigureModes(o), ErrCheckCPIRoleFlagConflict)
	}

	require.ErrorIs(t, validateConfigureModes(&configureOptions{pmxContext: "lab"}), ErrPmxContextWithoutCheck)
	require.ErrorIs(t, validateConfigureModes(&configureOptions{pruneDuplicates: true, pmxContext: "lab"}), ErrPmxContextWithoutCheck)
}

func TestConfigureCmdHasCheckCPIRoleFlags(t *testing.T) {
	t.Parallel()

	cmd := NewConfigureCmd()
	assert.NotNil(t, cmd.Flags().Lookup("check-cpi-role"))
	assert.NotNil(t, cmd.Flags().Lookup("pmx-context"))
	assert.Contains(t, cmd.Example, "--check-cpi-role")
}
