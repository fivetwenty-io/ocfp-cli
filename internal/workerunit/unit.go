package workerunit

import (
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// The worker's unit comes in two files, as the inception vault's does. The
// template, bloc-worker@.service, is the same for every bloc and names no
// user. Each bloc's instance gets a drop-in,
// bloc-worker@<bloc>.service.d/operator.conf, that names the operator it
// runs as and the worker's directory, and an instance without that drop-in
// never starts.
const (
	// TemplateName is the templated unit; each bloc runs as one instance.
	TemplateName = "bloc-worker@.service"

	// UnitPath is where the template is installed.
	UnitPath = unitDir + TemplateName

	// BinaryPath is where the worker binary is installed, owned by root
	// with mode 0755.
	BinaryPath = "/usr/local/bin/bloc-worker"

	// DataRoot holds each bloc's worker directory. It is on the bastion's
	// persistent data disk, so the worker's key and credential survive an
	// operating system replacement.
	DataRoot = "/data/bloc-worker"

	// ChangeMarker starts every line the install scripts print when they
	// change something on the bastion, so the phase knows to restart the
	// worker.
	ChangeMarker = "worker-change:"

	// unitDir is the directory systemd reads administrator units from.
	unitDir = "/etc/systemd/system/"

	// unitPrefix and unitSuffix wrap a bloc name into its instance name.
	unitPrefix = "bloc-worker@"
	unitSuffix = ".service"

	// dropInName is the instance's drop-in that names its operator.
	dropInName = "operator.conf"

	// unitPATH is the PATH the worker runs with, the same one the inception
	// vault's unit uses, because non-interactive sessions on bastions have
	// dropped linuxbrew's bin directory before.
	unitPATH = "/home/linuxbrew/.linuxbrew/bin:/home/linuxbrew/.linuxbrew/sbin:" +
		"/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

var (
	// ErrInvalidBloc reports a bloc name that cannot name a worker unit
	// instance or directory as written.
	ErrInvalidBloc = errors.New("the bloc name cannot name a bloc-worker unit instance")

	// ErrInvalidUnitParams reports a user or home that would break the unit
	// file or run the worker as the wrong user.
	ErrInvalidUnitParams = errors.New("invalid bloc-worker unit parameters")
)

var (
	// userPattern is the portable shape of a Linux user name.
	userPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

	// pathPattern is an absolute path with no character a unit file would
	// split on, quote, or expand.
	pathPattern = regexp.MustCompile(`^/[A-Za-z0-9_./+-]*$`)
)

// UnitParams are the bastion-specific values the drop-in is rendered with.
type UnitParams struct {
	// User is the operator user the worker runs as, the same user the
	// inception vault's unit runs as.
	User string
	// Home is that user's home directory.
	Home string
}

// Unit is the rendered unit, as the two files bastion init installs.
type Unit struct {
	// Template is the text of bloc-worker@.service.
	Template string
	// DropIn is the text of the bloc instance's operator.conf drop-in.
	DropIn string
}

// unitTemplate is the bloc-worker@.service text. It has no placeholders,
// because each instance's paths come from %i, which UnitName keeps to
// characters systemd takes literally.
const unitTemplate = `# Managed by OCFP. 'ocfp init bastion' writes this file and overwrites it
# on every run.
#
# Each instance, bloc-worker@<bloc>.service, runs that bloc's worker. On
# its first start the worker asks to join the bloc, and it runs once an
# operator approves the request in the bloc's workers panel.
#
# The user an instance runs as is set in that instance's drop-in,
# bloc-worker@<bloc>.service.d/operator.conf, which 'ocfp init bastion'
# writes next to this file. An instance without that drop-in never starts,
# so this template never runs the worker as root.
[Unit]
Description=OCFP bloc worker for bloc %i
After=network-online.target ocfp-dataset.service
Wants=network-online.target
RequiresMountsFor=/data
StartLimitIntervalSec=0
ConditionPathExists=/etc/systemd/system/bloc-worker@%i.service.d/operator.conf

[Service]
Type=simple
ExecStart=/usr/local/bin/bloc-worker run --dir /data/bloc-worker/%i --bastion-record /data/bloc-worker/%i/bastion.json
# A core outage or a network loss never leaves the worker stopped. Exit 3
# means the worker was removed and exit 4 that its request was rejected,
# and a worker turned away stays down until bastion init runs again.
Restart=on-failure
RestartSec=10
RestartPreventExitStatus=3 4
NoNewPrivileges=yes
ProtectSystem=strict
PrivateTmp=yes
ReadWritePaths=/data/bloc-worker/%i

[Install]
WantedBy=multi-user.target
`

// dropInTemplate is the text of an instance's operator.conf drop-in. The
// placeholders are filled by Render with values it has validated.
const dropInTemplate = `# Managed by OCFP. 'ocfp init bastion' writes this file and overwrites it
# on every run. It names the operator this bloc's worker runs as and the
# worker's directory.
[Unit]
# The instance starts only once bastion init has written its record.
ConditionPathExists={{DIR}}/bastion.json

[Service]
User={{USER}}
WorkingDirectory={{DIR}}
Environment=HOME={{HOME}}
Environment=PATH={{PATH}}
`

// installScript writes the unit's two files, the way the inception vault's
// boot unit is installed. It decodes them into a temporary directory under
// the instance's own name, verifies them when systemd-analyze is there,
// and only then installs them. It prints a change line when either file
// differs from the installed one.
const installScript = `set -euo pipefail
tmp="$(mktemp -d)"
trap "rm -rf -- \"$tmp\"" EXIT
mkdir -p "$tmp/{{NAME}}.d"
echo {{TEMPLATE}} | base64 -d > "$tmp/{{NAME}}"
echo {{DROPIN}} | base64 -d > "$tmp/{{NAME}}.d/{{DROPIN_NAME}}"
if command -v systemd-analyze >/dev/null 2>&1; then
  verify_args=()
  if systemd-analyze --help 2>/dev/null | grep -q -- --recursive-errors; then
    verify_args+=(--recursive-errors=no)
  fi
  systemd-analyze verify "${verify_args[@]}" "$tmp/{{NAME}}"
fi
if ! cmp -s "$tmp/{{NAME}}" {{UNIT_PATH}} || ! cmp -s "$tmp/{{NAME}}.d/{{DROPIN_NAME}}" {{DROPIN_PATH}}; then
  echo "{{MARKER}} unit"
fi
sudo install -m 0644 "$tmp/{{NAME}}" {{UNIT_PATH}}
sudo install -d -m 0755 {{DROPIN_DIR}}
sudo install -m 0644 "$tmp/{{NAME}}.d/{{DROPIN_NAME}}" {{DROPIN_PATH}}
sudo systemctl daemon-reload
sudo systemctl enable {{NAME}}`

// Render returns the template and the drop-in for bloc's worker on one
// bastion's operator.
func Render(bloc string, params UnitParams) (Unit, error) {
	dir, err := Dir(bloc)
	if err != nil {
		return Unit{}, err
	}

	err = params.validate()
	if err != nil {
		return Unit{}, err
	}

	replacer := strings.NewReplacer(
		"{{DIR}}", dir,
		"{{HOME}}", params.Home,
		"{{USER}}", params.User,
		"{{PATH}}", unitPATH,
	)

	return Unit{
		Template: unitTemplate,
		DropIn:   replacer.Replace(dropInTemplate),
	}, nil
}

// validate refuses values that would break the unit file or run the worker
// as root.
func (p UnitParams) validate() error {
	switch {
	case !userPattern.MatchString(p.User) || p.User == "root":
		return fmt.Errorf("%w: user %q must be the operator's own user name", ErrInvalidUnitParams, p.User)
	case !pathPattern.MatchString(p.Home) || filepath.Clean(p.Home) == "/":
		return fmt.Errorf("%w: home %q must be an absolute path with no spaces or special characters",
			ErrInvalidUnitParams, p.Home)
	}

	return nil
}

// UnitName returns the instance of the unit that serves bloc.
func UnitName(bloc string) (string, error) {
	if !blocForm.MatchString(bloc) {
		return "", fmt.Errorf("%w: %q", ErrInvalidBloc, bloc)
	}

	return unitPrefix + bloc + unitSuffix, nil
}

// DropInPath returns where bloc's instance keeps its operator.conf drop-in.
func DropInPath(bloc string) (string, error) {
	name, err := UnitName(bloc)
	if err != nil {
		return "", err
	}

	return unitDir + name + ".d/" + dropInName, nil
}

// Dir returns bloc's worker directory.
func Dir(bloc string) (string, error) {
	if !blocForm.MatchString(bloc) {
		return "", fmt.Errorf("%w: %q", ErrInvalidBloc, bloc)
	}

	return DataRoot + "/" + bloc, nil
}

// InstallCommand returns the shell command that installs unit's template
// at UnitPath and its drop-in at DropInPath(bloc), reloads systemd, and
// enables bloc's instance without starting it. The script holds no single
// quote, so it is safe inside bash -c '...'.
func InstallCommand(bloc string, unit Unit) (string, error) {
	name, err := UnitName(bloc)
	if err != nil {
		return "", err
	}

	dropInPath, err := DropInPath(bloc)
	if err != nil {
		return "", err
	}

	script := strings.NewReplacer(
		"{{NAME}}", name,
		"{{TEMPLATE}}", base64.StdEncoding.EncodeToString([]byte(unit.Template)),
		"{{DROPIN}}", base64.StdEncoding.EncodeToString([]byte(unit.DropIn)),
		"{{DROPIN_NAME}}", dropInName,
		"{{UNIT_PATH}}", UnitPath,
		"{{DROPIN_DIR}}", filepath.Dir(dropInPath),
		"{{DROPIN_PATH}}", dropInPath,
		"{{MARKER}}", ChangeMarker,
	).Replace(installScript)

	return "bash -c '" + script + "'", nil
}

// StartCommand returns the command that starts bloc's worker, or restarts
// it when restart is set. Starting a worker that is already running
// changes nothing.
func StartCommand(bloc string, restart bool) (string, error) {
	name, err := UnitName(bloc)
	if err != nil {
		return "", err
	}

	verb := "start"
	if restart {
		verb = "restart"
	}

	return "sudo systemctl " + verb + " " + name, nil
}
