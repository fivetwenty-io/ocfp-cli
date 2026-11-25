package workerunit_test

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fivetwenty-io/ocfp-cli/internal/workerunit"
)

const testBloc = "ocfp-lab-drgao"

func renderTestUnit(t *testing.T) workerunit.Unit {
	t.Helper()

	unit, err := workerunit.Render(testBloc, workerunit.UnitParams{User: "ubuntu", Home: "/home/ubuntu"})
	require.NoError(t, err)

	return unit
}

func TestNames(t *testing.T) {
	t.Parallel()

	name, err := workerunit.UnitName(testBloc)
	require.NoError(t, err)
	assert.Equal(t, "bloc-worker@ocfp-lab-drgao.service", name)

	dropIn, err := workerunit.DropInPath(testBloc)
	require.NoError(t, err)
	assert.Equal(t, "/etc/systemd/system/bloc-worker@ocfp-lab-drgao.service.d/operator.conf", dropIn)

	dir, err := workerunit.Dir(testBloc)
	require.NoError(t, err)
	assert.Equal(t, "/data/bloc-worker/ocfp-lab-drgao", dir)

	assert.Equal(t, "/etc/systemd/system/bloc-worker@.service", workerunit.UnitPath)
	assert.Equal(t, "/usr/local/bin/bloc-worker", workerunit.BinaryPath)

	for _, bad := range []string{"", "bad/bloc", "Ocfp-Lab", "ocfp-lab drgao", "ocfp-%i", "../x"} {
		_, err := workerunit.UnitName(bad)
		require.ErrorIs(t, err, workerunit.ErrInvalidBloc, bad)

		_, err = workerunit.Dir(bad)
		require.ErrorIs(t, err, workerunit.ErrInvalidBloc, bad)
	}
}

func TestRender_Template(t *testing.T) {
	t.Parallel()

	tmpl := renderTestUnit(t).Template

	for _, line := range []string{
		"After=network-online.target ocfp-dataset.service",
		"Wants=network-online.target",
		"RequiresMountsFor=/data",
		"StartLimitIntervalSec=0",
		"ConditionPathExists=/etc/systemd/system/bloc-worker@%i.service.d/operator.conf",
		"ExecStart=/usr/local/bin/bloc-worker run --dir /data/bloc-worker/%i --bastion-record /data/bloc-worker/%i/bastion.json",
		"Restart=on-failure",
		"RestartSec=10",
		"RestartPreventExitStatus=3 4",
		"NoNewPrivileges=yes",
		"ProtectSystem=strict",
		"ReadWritePaths=/data/bloc-worker/%i",
		"WantedBy=multi-user.target",
	} {
		assert.Contains(t, tmpl, "\n"+line+"\n", "the template sets %s", line)
	}

	assert.NotContains(t, tmpl, "User=", "the template names no user, so it never runs as root by itself")
}

func TestRender_DropIn(t *testing.T) {
	t.Parallel()

	dropIn := renderTestUnit(t).DropIn

	for _, line := range []string{
		"User=ubuntu",
		"WorkingDirectory=/data/bloc-worker/ocfp-lab-drgao",
		"Environment=HOME=/home/ubuntu",
		"Environment=PATH=/home/linuxbrew/.linuxbrew/bin:/home/linuxbrew/.linuxbrew/sbin:" +
			"/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"ConditionPathExists=/data/bloc-worker/ocfp-lab-drgao/bastion.json",
	} {
		assert.Contains(t, dropIn, "\n"+line+"\n", "the drop-in sets %s", line)
	}
}

func TestRender_Refusals(t *testing.T) {
	t.Parallel()

	for name, params := range map[string]workerunit.UnitParams{
		"root":         {User: "root", Home: "/root"},
		"no user":      {User: "", Home: "/home/ubuntu"},
		"odd user":     {User: "Ubuntu User", Home: "/home/ubuntu"},
		"relative":     {User: "ubuntu", Home: "home/ubuntu"},
		"root home":    {User: "ubuntu", Home: "/"},
		"spaced home":  {User: "ubuntu", Home: "/home/my user"},
		"newline user": {User: "ubuntu\nUser=root", Home: "/home/ubuntu"},
	} {
		_, err := workerunit.Render(testBloc, params)
		require.ErrorIs(t, err, workerunit.ErrInvalidUnitParams, name)
	}

	_, err := workerunit.Render("bad/bloc", workerunit.UnitParams{User: "ubuntu", Home: "/home/ubuntu"})
	require.ErrorIs(t, err, workerunit.ErrInvalidBloc)
}

// decodeInstalled pulls a base64 payload the install script writes to the
// temporary path that ends with suffix.
func decodeInstalled(t *testing.T, cmd, suffix string) string {
	t.Helper()

	pattern := regexp.MustCompile(`echo ([A-Za-z0-9+/=]+) \| base64 -d > "\$tmp/` + regexp.QuoteMeta(suffix) + `"`)
	match := pattern.FindStringSubmatch(cmd)
	require.Len(t, match, 2, "the install command carries %s base64-encoded", suffix)

	text, err := base64.StdEncoding.DecodeString(match[1])
	require.NoError(t, err)

	return string(text)
}

func TestInstallCommand(t *testing.T) {
	t.Parallel()

	unit := renderTestUnit(t)

	cmd, err := workerunit.InstallCommand(testBloc, unit)
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(cmd, "bash -c '"))
	body := strings.TrimSuffix(strings.TrimPrefix(cmd, "bash -c '"), "'")
	assert.NotContains(t, body, "'", "the script holds no single quote, so bash -c '...' carries it whole")

	assert.Equal(t, unit.Template, decodeInstalled(t, cmd, "bloc-worker@ocfp-lab-drgao.service"))
	assert.Equal(t, unit.DropIn, decodeInstalled(t, cmd, "bloc-worker@ocfp-lab-drgao.service.d/operator.conf"))

	assert.Contains(t, cmd, "systemd-analyze verify")
	assert.Contains(t, cmd, "sudo install -m 0644 \"$tmp/bloc-worker@ocfp-lab-drgao.service\" /etc/systemd/system/bloc-worker@.service")
	assert.Contains(t, cmd, "/etc/systemd/system/bloc-worker@ocfp-lab-drgao.service.d/operator.conf")
	assert.Contains(t, cmd, "sudo systemctl daemon-reload")
	assert.Contains(t, cmd, "sudo systemctl enable bloc-worker@ocfp-lab-drgao.service")
	assert.Contains(t, cmd, workerunit.ChangeMarker+" unit", "the script reports when it changed the unit")
	assert.NotContains(t, cmd, "--now", "the phase decides when to start or restart")

	_, err = workerunit.InstallCommand("bad/bloc", unit)
	require.ErrorIs(t, err, workerunit.ErrInvalidBloc)
}

func TestStartCommand(t *testing.T) {
	t.Parallel()

	start, err := workerunit.StartCommand(testBloc, false)
	require.NoError(t, err)
	assert.Equal(t, "sudo systemctl start bloc-worker@ocfp-lab-drgao.service", start)

	restart, err := workerunit.StartCommand(testBloc, true)
	require.NoError(t, err)
	assert.Equal(t, "sudo systemctl restart bloc-worker@ocfp-lab-drgao.service", restart)

	_, err = workerunit.StartCommand("bad/bloc", true)
	require.ErrorIs(t, err, workerunit.ErrInvalidBloc)
}
