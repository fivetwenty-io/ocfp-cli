package vaultboot

import (
	"context"
	"encoding/base64"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testParams() UnitParams {
	return UnitParams{User: "ubuntu", Home: "/home/ubuntu", OCFPPath: "/usr/local/bin/ocfp"}
}

func TestRender_TemplateRunsVaultStartOnceAndNamesNoOperator(t *testing.T) {
	t.Parallel()

	unit, err := Render(testParams())
	require.NoError(t, err)

	lines := strings.Split(unit.Template, "\n")
	for _, line := range []string{
		"[Unit]",
		"After=ocfp-dataset.service network-online.target",
		"Wants=network-online.target",
		"ConditionPathExists=/etc/systemd/system/ocfp-vault@%i.service.d/operator.conf",
		"[Service]",
		"Type=oneshot",
		"RemainAfterExit=yes",
		"ExecStart=/usr/local/bin/ocfp vault start --bloc %i",
		"Restart=no",
		"KillMode=process",
		"[Install]",
		"WantedBy=multi-user.target",
	} {
		assert.Contains(t, lines, line)
	}

	for _, operator := range []string{"ubuntu", "User=", "HOME", "WorkingDirectory=", "ConditionPathExists=|"} {
		assert.NotContains(t, unit.Template, operator, "the template is shared by every bloc and names no operator")
	}

	assert.Contains(t, unit.Template, "'ocfp init bastion' writes this file")
	assert.NotContains(t, unit.Template, "%I", "the instance is the bloc name as written; %I would turn its dashes into slashes")
	assert.NotRegexp(t, regexp.MustCompile(`(?m)^Restart=(always|on-)`), unit.Template, "a failure must not loop")
	assert.NotContains(t, unit.Template, "vault inception", "the unit must never run the command that can archive")
	assert.True(t, strings.HasSuffix(unit.Template, "\n"))
}

func TestRender_DropInNamesTheOperatorAndTheKeysToWaitFor(t *testing.T) {
	t.Parallel()

	unit, err := Render(testParams())
	require.NoError(t, err)

	lines := strings.Split(unit.DropIn, "\n")
	for _, line := range []string{
		"[Unit]",
		"ConditionPathExists=|/home/ubuntu/.local/share/ocfp/%i/vault/unseal.keys",
		"ConditionPathExists=|/home/ubuntu/.ocfp/%i/vault/unseal.keys",
		"[Service]",
		"User=ubuntu",
		"WorkingDirectory=/home/ubuntu",
		"Environment=HOME=/home/ubuntu",
	} {
		assert.Contains(t, lines, line)
	}

	assert.NotContains(t, unit.DropIn, "ExecStart", "the drop-in only names the operator")
	assert.Contains(t, unit.DropIn, "'ocfp init bastion' writes this file")
	assert.True(t, strings.HasSuffix(unit.DropIn, "\n"))
}

// Non-interactive sessions on bastions have dropped linuxbrew's bin
// directory before, and safe, tmux, and the engine live there.
func TestRender_PathHasLinuxbrewAndTheSystemDirectories(t *testing.T) {
	t.Parallel()

	unit, err := Render(testParams())
	require.NoError(t, err)

	match := regexp.MustCompile(`(?m)^Environment=PATH=(.*)$`).FindStringSubmatch(unit.DropIn)
	require.Len(t, match, 2, "the unit sets PATH")

	dirs := strings.Split(match[1], ":")
	for _, dir := range []string{"/home/linuxbrew/.linuxbrew/bin", "/usr/local/bin", "/usr/bin", "/bin"} {
		assert.Contains(t, dirs, dir)
	}
}

func TestRender_RejectsValuesThatWouldBreakTheUnit(t *testing.T) {
	t.Parallel()

	for name, mutate := range map[string]func(p *UnitParams){
		"empty user":        func(p *UnitParams) { p.User = "" },
		"user with a space": func(p *UnitParams) { p.User = "ubu ntu" },
		"root user":         func(p *UnitParams) { p.User = "root" },
		"relative home":     func(p *UnitParams) { p.Home = "home/ubuntu" },
		"home with newline": func(p *UnitParams) { p.Home = "/home/ubuntu\nUser=root" },
		"home with percent": func(p *UnitParams) { p.Home = "/home/%i" },
		"root home":         func(p *UnitParams) { p.Home = "/" },
		"relative ocfp":     func(p *UnitParams) { p.OCFPPath = "ocfp" },
		"ocfp with a space": func(p *UnitParams) { p.OCFPPath = "/usr/local/bin/oc fp" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			params := testParams()
			mutate(&params)

			_, err := Render(params)
			require.ErrorIs(t, err, ErrInvalidUnitParams)
		})
	}
}

func TestUnitName_NamesTheBlocInstance(t *testing.T) {
	t.Parallel()

	name, err := UnitName("ocfp-lab-drgao")
	require.NoError(t, err)
	assert.Equal(t, "ocfp-vault@ocfp-lab-drgao.service", name)

	for _, bloc := range []string{"", "a/b", "a b", "a\\x2d", "-lead", "a%i", "a'b"} {
		_, err := UnitName(bloc)
		require.ErrorIs(t, err, ErrInvalidBloc, bloc)
	}
}

func TestDropInPath_SitsInTheBlocInstancesDropInDirectory(t *testing.T) {
	t.Parallel()

	path, err := DropInPath("ocfp-lab-drgao")
	require.NoError(t, err)
	assert.Equal(t, "/etc/systemd/system/ocfp-vault@ocfp-lab-drgao.service.d/operator.conf", path)

	_, err = DropInPath("a/b")
	require.ErrorIs(t, err, ErrInvalidBloc)
}

// decodedFile returns the text that the install script decodes into the
// temporary file whose name ends with suffix.
func decodedFile(t *testing.T, script, suffix string) string {
	t.Helper()

	pattern := regexp.MustCompile(`echo ([A-Za-z0-9+/=]+) \| base64 -d > "\$tmp/` + regexp.QuoteMeta(suffix) + `"`)
	match := pattern.FindStringSubmatch(script)
	require.Len(t, match, 2, "the script decodes %s into the temporary directory", suffix)

	decoded, err := base64.StdEncoding.DecodeString(match[1])
	require.NoError(t, err)

	return string(decoded)
}

func TestInstallCommand_VerifiesThenInstallsBothFilesAndEnablesTheInstance(t *testing.T) {
	t.Parallel()

	unit, err := Render(testParams())
	require.NoError(t, err)

	cmd, err := InstallCommand("ocfp-lab-drgao", unit)
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(cmd, "bash -c '"), cmd)
	require.True(t, strings.HasSuffix(cmd, "'"), cmd)
	script := strings.TrimSuffix(strings.TrimPrefix(cmd, "bash -c '"), "'")
	assert.NotContains(t, script, "'", "a single quote would end the bash -c argument early")

	assert.Equal(t, unit.Template, decodedFile(t, script, "ocfp-vault@ocfp-lab-drgao.service"))
	assert.Equal(t, unit.DropIn, decodedFile(t, script, "ocfp-vault@ocfp-lab-drgao.service.d/operator.conf"))

	steps := []string{
		"set -euo pipefail",
		`tmp="$(mktemp -d)"`,
		`trap "rm -rf -- \"$tmp\"" EXIT`,
		"if command -v systemd-analyze >/dev/null 2>&1; then",
		`systemd-analyze verify "${verify_args[@]}" "$tmp/ocfp-vault@ocfp-lab-drgao.service"`,
		`sudo install -m 0644 "$tmp/ocfp-vault@ocfp-lab-drgao.service" /etc/systemd/system/ocfp-vault@.service`,
		"sudo install -d -m 0755 /etc/systemd/system/ocfp-vault@ocfp-lab-drgao.service.d",
		`sudo install -m 0644 "$tmp/ocfp-vault@ocfp-lab-drgao.service.d/operator.conf" ` +
			"/etc/systemd/system/ocfp-vault@ocfp-lab-drgao.service.d/operator.conf",
		"sudo systemctl daemon-reload",
		"sudo systemctl enable ocfp-vault@ocfp-lab-drgao.service",
	}

	last := -1
	for _, step := range steps {
		at := strings.Index(script, step)
		require.NotEqual(t, -1, at, "the script is missing %q:\n%s", step, script)
		assert.Greater(t, at, last, "%q is out of order", step)
		last = at
	}

	assert.NotContains(t, script, "sudo tee", "nothing is written in place, so a failed decode leaves the old unit")
	assert.NotContains(t, cmd, "--now", "enabling must not start a vault during init")
	assert.NotContains(t, cmd, "systemctl start")

	_, err = InstallCommand("a/b", unit)
	require.ErrorIs(t, err, ErrInvalidBloc)
}

// The script must pass bash's own parser, or a quoting slip would only show
// up on a bastion.
func TestInstallCommand_ScriptParses(t *testing.T) {
	t.Parallel()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}

	unit, err := Render(testParams())
	require.NoError(t, err)

	cmd, err := InstallCommand("ocfp-lab-drgao", unit)
	require.NoError(t, err)

	script := strings.TrimSuffix(strings.TrimPrefix(cmd, "bash -c '"), "'")
	out, err := exec.CommandContext(context.Background(), bash, "-n", "-c", script).CombinedOutput()
	require.NoError(t, err, string(out))
}

func TestSupportCheckCommand_AsksTheBinaryForVaultStart(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "/usr/local/bin/ocfp vault start --check-support", SupportCheckCommand("/usr/local/bin/ocfp"))
}
