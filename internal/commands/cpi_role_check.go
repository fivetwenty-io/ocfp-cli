package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
)

// Errors reported while checking the CPI role.
var (
	ErrPmxNotInstalled          = errors.New("pmx is not installed or not on PATH")
	ErrPmxFailed                = errors.New("pmx failed")
	ErrNoPrivilegesToSet        = errors.New("refusing to build a role set command with no privileges")
	ErrCPIRoleNotFound          = errors.New("the OCFPCpi role does not exist")
	ErrCPIRolePrivsMissing      = errors.New("the OCFPCpi role is missing privileges")
	ErrCPIRoleStillMissing      = errors.New("the OCFPCpi role is still missing privileges after the update")
	ErrConfigureModeConflict    = errors.New("--prune-duplicate-rules and --check-cpi-role cannot be combined")
	ErrCheckCPIRoleFlagConflict = errors.New("--check-cpi-role runs on its own and cannot be combined with other configure options")
	ErrPmxContextWithoutCheck   = errors.New("--pmx-context only applies to --check-cpi-role")
)

// pmxEndpointEnv is the variable that makes pmx talk to a different endpoint
// than the context names. `pmx context show` ignores it and role get and
// role set honour it.
const pmxEndpointEnv = "PMX_API_ENDPOINT"

// pmxArgs prefixes args with -c <context> when a context was named.
func pmxArgs(pmxContext string, args ...string) []string {
	if pmxContext == "" {
		return args
	}

	return append([]string{"-c", pmxContext}, args...)
}

// cpiRoleGetArgs builds the arguments that read the role as JSON.
func cpiRoleGetArgs(pmxContext string) []string {
	return pmxArgs(pmxContext, "pve", "access", "role", "get", cpiRoleID, "-o", "json")
}

// cpiRoleSetArgs builds the only `role set` command this package ever runs.
// It always carries --append, because without it pmx replaces every
// privilege the role holds.
func cpiRoleSetArgs(pmxContext string, missing []string) ([]string, error) {
	if len(missing) == 0 {
		return nil, ErrNoPrivilegesToSet
	}

	return pmxArgs(pmxContext, "pve", "access", "role", "set", cpiRoleID,
		"--privs", strings.Join(missing, ","), "--append"), nil
}

// pmxStderrText turns captured pmx output into one trimmed line for errors.
func pmxStderrText(stderr []byte, err error) string {
	text := strings.TrimSpace(string(stderr))
	if text == "" {
		return err.Error()
	}

	return text
}

// describePmxContext asks pmx which context and endpoint a call will use.
// It is informational, so any failure yields "unknown" instead of an error.
func describePmxContext(ctx context.Context, pmxContext string) (name, endpoint string) {
	name, endpoint = describeStoredContext(ctx, pmxContext)

	// pmx context show reports the stored context only, but the later calls
	// honour PMX_API_ENDPOINT, so name the endpoint they will really use.
	if override := os.Getenv(pmxEndpointEnv); override != "" {
		endpoint = override + " (" + pmxEndpointEnv + " overrides the context's endpoint)"
	}

	return name, endpoint
}

// describeStoredContext reads the context's own name and endpoint.
func describeStoredContext(ctx context.Context, pmxContext string) (name, endpoint string) {
	stdout, _, err := runner.RunSplit(ctx, "pmx", pmxArgs(pmxContext, "context", "show", "-o", "json")...)
	if err != nil {
		return orUnknown(pmxContext), "unknown"
	}

	var info struct {
		Name     string `json:"name"`
		Host     string `json:"host"`
		Port     int    `json:"port"`
		Protocol string `json:"protocol"`
	}

	if json.Unmarshal(stdout, &info) != nil || info.Host == "" {
		return orUnknown(pmxContext), "unknown"
	}

	return orUnknown(info.Name), fmt.Sprintf("%s://%s:%d", info.Protocol, info.Host, info.Port)
}

// countPrivileges renders a count with the right singular or plural noun.
func countPrivileges(n int) string {
	if n == 1 {
		return "1 privilege"
	}

	return fmt.Sprintf("%d privileges", n)
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}

	return s
}

// readCPIRole returns the set of privileges the role currently grants.
func readCPIRole(ctx context.Context, pmxContext string) (map[string]bool, error) {
	stdout, stderr, err := runner.RunSplit(ctx, "pmx", cpiRoleGetArgs(pmxContext)...)
	if err != nil {
		text := pmxStderrText(stderr, err)

		// PVE answers a missing role with its own message, and pmx prints it
		// on stderr. Anything else, such as an unknown pmx context or a 404
		// from a proxy, is a pmx failure and not a missing role.
		gone := fmt.Sprintf("role '%s' does not exist", cpiRoleID)
		if strings.Contains(strings.ToLower(string(stderr)), strings.ToLower(gone)) {
			return nil, fmt.Errorf("%w; ocfp does not create it, so create it with the steps in %s", ErrCPIRoleNotFound, cpiRunbook)
		}

		return nil, fmt.Errorf("%w reading role %s: %s", ErrPmxFailed, cpiRoleID, text)
	}

	// pmx prints the role as a JSON object that maps each privilege name to
	// a boolean. The API's integers become JSON booleans because pmx decodes
	// each field through the apiclient's PVEBool (internal/cli/access/role.go,
	// newRoleGetCmd, which prints the typed response).
	var flags map[string]bool

	err = json.Unmarshal(stdout, &flags)
	if err != nil {
		return nil, fmt.Errorf("%w: could not read role %s output as JSON: %w", ErrPmxFailed, cpiRoleID, err)
	}

	have := make(map[string]bool, len(flags))

	for name, on := range flags {
		if on {
			have[name] = true
		}
	}

	return have, nil
}

// diffCPIRole returns the canonical privileges the role lacks, in runbook
// order, and the privileges it holds beyond the canonical list, sorted.
func diffCPIRole(have map[string]bool) (missing, extra []string) {
	for _, p := range cpiRolePrivileges {
		if !have[p] {
			missing = append(missing, p)
		}
	}

	for p := range have {
		if !slices.Contains(cpiRolePrivileges, p) {
			extra = append(extra, p)
		}
	}

	sort.Strings(extra)

	return missing, extra
}

// runCheckCPIRole reports whether the OCFPCpi role holds every privilege the
// CPI needs. With apply it appends the missing ones, then reads the role
// again to confirm. It never removes a privilege and never creates the role.
func runCheckCPIRole(ctx context.Context, pmxContext string, apply bool, out io.Writer) error {
	if runner.LookPath("pmx") != nil {
		return fmt.Errorf("%w; install pmx before running --check-cpi-role", ErrPmxNotInstalled)
	}

	say := func(format string, args ...any) { _, _ = fmt.Fprintf(out, format, args...) }

	ctxName, endpoint := describePmxContext(ctx, pmxContext)

	say("Checking role %s on pmx context %s (%s)\n", cpiRoleID, ctxName, endpoint)

	have, err := readCPIRole(ctx, pmxContext)
	if err != nil {
		return err
	}

	missing, extra := diffCPIRole(have)

	if len(extra) > 0 {
		say("Extra privileges, not a problem and left alone: %s\n", strings.Join(extra, ", "))
	}

	if len(missing) == 0 {
		say("Role %s holds all %s required.\n", cpiRoleID, countPrivileges(len(cpiRolePrivileges)))

		return nil
	}

	setArgs, err := cpiRoleSetArgs(pmxContext, missing)
	if err != nil {
		return err
	}

	fix := "pmx " + strings.Join(setArgs, " ")

	say("Role %s is missing %s: %s\n", cpiRoleID, countPrivileges(len(missing)), strings.Join(missing, ", "))

	if !apply {
		say("Run again with --apply to run this command, which only adds privileges:\n  %s\n", fix)

		return fmt.Errorf("%w: %s", ErrCPIRolePrivsMissing, strings.Join(missing, ", "))
	}

	say("Running: %s\n", fix)

	_, stderr, err := runner.RunSplit(ctx, "pmx", setArgs...)
	if err != nil {
		return fmt.Errorf("%w updating role %s: %s", ErrPmxFailed, cpiRoleID, pmxStderrText(stderr, err))
	}

	have, err = readCPIRole(ctx, pmxContext)
	if err != nil {
		return err
	}

	stillMissing, _ := diffCPIRole(have)
	if len(stillMissing) > 0 {
		say("Role %s is still missing: %s\n", cpiRoleID, strings.Join(stillMissing, ", "))

		return fmt.Errorf("%w: %s", ErrCPIRoleStillMissing, strings.Join(stillMissing, ", "))
	}

	say("Role %s now holds all %s required.\n", cpiRoleID, countPrivileges(len(cpiRolePrivileges)))

	return nil
}
