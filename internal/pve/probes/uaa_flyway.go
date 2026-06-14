package probes

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// reFailedRows matches the "FAILED_ROWS=N" sentinel and captures N.
var reFailedRows = regexp.MustCompile(`FAILED_ROWS=(\d+)`)

// uaaRemoteScript is the bash one-liner injected into database/0 via
// `bosh ssh -c`. It emits exactly one sentinel token per run. The script is
// reproduced verbatim from the lab cmd_check_uaa_db() source.
//
// Token semantics:
//
//	SKIP_NO_PXC        — PXC job config absent; CF not yet deployed (OK)
//	SKIP_NO_MYSQL_BIN  — percona mysql binary not found (OK)
//	OK_DB_MISSING      — "uaa" database not yet created (OK)
//	OK_FRESH           — schema_version table not yet created (OK)
//	PROBE_ERROR:<msg>  — mysql returned an unrecognised error (non-fatal)
//	FAILED_ROWS=N      — N rows with success=0 found; remediation required
const uaaRemoteScript = "" +
	"set -e; " +
	"cnf=/var/vcap/jobs/pxc-mysql/config/mylogin.cnf; " +
	"if [ ! -f $cnf ]; then echo SKIP_NO_PXC; exit 0; fi; " +
	"mysql=$(find /var/vcap/data/packages -path '*percona-xtradb-cluster*/bin/mysql' " +
	"| sort | tail -1); " +
	"if [ -z \"$mysql\" ]; then echo SKIP_NO_MYSQL_BIN; exit 0; fi; " +
	"q='SELECT COUNT(*) FROM schema_version WHERE success=0'; " +
	"out=$(sudo $mysql --defaults-file=$cnf uaa -BNe \"$q\" 2>&1) || { echo \"$out\" | grep -q 'Unknown database' " +
	"&& { echo OK_DB_MISSING; exit 0; }; echo \"$out\" | grep -q \"doesn.t exist\" " +
	"&& { echo OK_FRESH; exit 0; }; echo PROBE_ERROR: $out; exit 0; }; " +
	"echo FAILED_ROWS=$out"

// uaaRemediationFmt is the operator remediation block. The caller substitutes
// %s with the BOSH environment name and deployment name so the operator can
// copy-paste the commands directly.
//
// The SQL sequence drops and recreates the "uaa" database so that UAA's Flyway
// migration runs cleanly on next boot. UAA rebuilds all seed data automatically.
const uaaRemediationFmt = `UAA Flyway history has %d failed migration row(s) (success=0).

A partial DDL has already been applied to the schema. The next UAA boot will
retry the migration, hit a duplicate-object error, and crashloop until the
BOSH agent's get_state RPC times out.

Remediation — run on database/0 (UAA rebuilds seed data on next boot):

  bosh -e %s -d %s ssh database/0 -c \
    "sudo \$(find /var/vcap/data/packages \
        -path '*percona-xtradb-cluster*/bin/mysql' | sort | tail -1) \
        --defaults-file=/var/vcap/jobs/pxc-mysql/config/mylogin.cnf \
        -e 'DROP DATABASE uaa; CREATE DATABASE uaa CHARACTER SET utf8mb4 \
        COLLATE utf8mb4_unicode_ci; GRANT ALL PRIVILEGES ON uaa.* TO uaa@\"%%\"; \
        FLUSH PRIVILEGES;'"

  bosh -e %s -d %s recreate uaa
`

// UAAFlywayProbe queries the UAA Flyway migration history table on database/0
// via bosh ssh. It returns FAIL with a remediation block when any
// schema_version row has success=0, indicating a partial DDL migration that
// will cause UAA to crashloop on the next deploy.
//
// All skip/fresh/missing-DB conditions are treated as OK: they indicate a
// new or not-yet-deployed CF environment where the probe cannot yet run.
// PROBE_ERROR is also treated as OK (non-fatal) per lab semantics, with the
// error detail recorded for operator visibility.
type UAAFlywayProbe struct {
	// RunBosh is the function used to execute bosh ssh. Tests inject a mock;
	// production code passes a closure over the real bosh binary.
	// ctx is propagated to exec.CommandContext so Ctrl-C cancels the subprocess.
	// args are passed directly to the bosh binary, e.g.:
	//   ["-e", env, "-d", dep, "ssh", "database/0", "-c", script, "-r"]
	RunBosh func(ctx context.Context, args ...string) ([]byte, error)

	// Deployment is the BOSH deployment name (e.g. "cf-lab").
	Deployment string

	// Env is the BOSH environment alias (e.g. "lab").
	Env string

	// Instance is the BOSH instance slug to ssh into (default "database/0").
	// When empty, "database/0" is used.
	Instance string
}

// Name implements Probe.
func (p *UAAFlywayProbe) Name() string {
	return "uaa-flyway"
}

// instance returns the configured instance or the default.
func (p *UAAFlywayProbe) instance() string {
	if p.Instance != "" {
		return p.Instance
	}

	return "database/0"
}

// Run implements Probe. It shells out via p.RunBosh and parses sentinel tokens
// from the combined stdout+stderr of the remote script.
//
// Failure modes and their behaviour:
//
//	RunBosh returns error AND output contains "doesn't exist"
//	  → OK (CF deployment not yet present on this director)
//	RunBosh returns any other error
//	  → OK with Detail="bosh ssh failed: ..." (non-fatal; log + continue)
//	Output contains SKIP_NO_PXC, SKIP_NO_MYSQL_BIN, OK_DB_MISSING, OK_FRESH
//	  → OK (expected pre-CF or fresh-install states)
//	Output contains PROBE_ERROR
//	  → OK with Detail=PROBE_ERROR line (non-fatal per lab semantic)
//	Output contains FAILED_ROWS=0
//	  → OK (no failed migrations)
//	Output contains FAILED_ROWS=N (N > 0)
//	  → FAIL with Remediation block
//	Output matches no known sentinel
//	  → FAIL with Detail="unparseable probe output"
func (p *UAAFlywayProbe) Run(ctx context.Context) Result {
	if p.RunBosh == nil {
		return Result{OK: false, Detail: "UAAFlywayProbe: RunBosh must not be nil"}
	}

	if p.Deployment == "" {
		return Result{OK: false, Detail: "UAAFlywayProbe: Deployment must not be empty"}
	}

	if p.Env == "" {
		return Result{OK: false, Detail: "UAAFlywayProbe: Env must not be empty"}
	}

	args := []string{
		"-e", p.Env,
		"-d", p.Deployment,
		"ssh", p.instance(),
		"-c", uaaRemoteScript,
		"-r",
	}

	out, err := p.RunBosh(ctx, args...)
	combined := string(out)

	if err != nil {
		// Deployment not yet present on this director → non-fatal.
		// Match both straight ASCII apostrophe and curly U+2019 apostrophe,
		// since bosh ssh stderr can be emitted in either form depending on locale.
		if strings.Contains(combined, "doesn't exist") || strings.Contains(combined, "doesn’t exist") {
			return Result{OK: true, Detail: "CF deployment not yet present; skipped"}
		}
		// Any other bosh ssh failure → non-fatal, log detail.
		return Result{
			OK:     true,
			Detail: fmt.Sprintf("bosh ssh failed (non-fatal): %v; output: %s", err, truncate(combined, 500)),
		}
	}

	// Check benign skip conditions first.
	for _, token := range []string{SentinelOKFresh, SentinelOKDBMissing, SentinelSkipNoPXC, SentinelSkipNoMySQLBin} {
		if strings.Contains(combined, token) {
			return Result{OK: true, Detail: token}
		}
	}

	// Non-fatal remote script error.
	if strings.Contains(combined, SentinelProbeError) {
		line := lastLineContaining(combined, SentinelProbeError)

		return Result{OK: true, Detail: line}
	}

	// Parse FAILED_ROWS=N.
	m := reFailedRows.FindStringSubmatch(combined)
	if m == nil {
		return Result{
			OK:     false,
			Detail: "probe output unparseable — no known sentinel found; raw (last 500): " + truncate(combined, 500),
		}
	}

	count, _ := strconv.Atoi(m[1]) // regex guarantees digits; Atoi cannot fail

	if count == 0 {
		return Result{OK: true, Detail: "FAILED_ROWS=0"}
	}

	remediation := fmt.Sprintf(uaaRemediationFmt, count, p.Env, p.Deployment, p.Env, p.Deployment)

	return Result{
		OK:          false,
		Detail:      fmt.Sprintf("FAILED_ROWS=%d", count),
		Remediation: remediation,
	}
}

// truncate returns s truncated to at most n bytes from the end.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[len(s)-n:]
}

// lastLineContaining returns the last line in s that contains substr, or
// substr itself when no line matches.
func lastLineContaining(s, substr string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], substr) {
			return strings.TrimSpace(lines[i])
		}
	}

	return substr
}
