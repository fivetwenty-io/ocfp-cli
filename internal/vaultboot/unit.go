// Package vaultboot renders the systemd unit that brings a bloc's inception
// vault back when a bastion boots, and names the per-bloc instance of it.
// Bastion init installs and enables the unit, and 'ocfp vault teardown'
// disables it, so both sides take the unit's name from here.
//
// The unit comes in two files. The template, ocfp-vault@.service, is the
// same for every bloc and names no user. Each bloc's instance gets a
// drop-in, ocfp-vault@<bloc>.service.d/operator.conf, that names the
// operator it runs as and the key files it waits for, so blocs on the same
// bastion can run as different users.
package vaultboot

import (
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// TemplateName is the templated unit; each bloc runs as one instance.
	TemplateName = "ocfp-vault@.service"

	// UnitPath is where the template is installed. It is a system unit
	// with User=, not a user unit, so it can be ordered after the system's
	// ocfp-dataset.service, which mounts the operator's home on PVE
	// bastions, and so it needs no lingering user manager.
	UnitPath = unitDir + TemplateName

	// unitDir is the directory systemd reads administrator units from.
	unitDir = "/etc/systemd/system/"

	// unitPrefix and unitSuffix wrap a bloc name into its instance name.
	unitPrefix = "ocfp-vault@"
	unitSuffix = ".service"

	// dropInName is the instance's drop-in that names its operator.
	dropInName = "operator.conf"

	// supportCheckArgs asks an ocfp binary whether it has 'vault start'.
	// The flag is the hidden --check-support flag of 'ocfp vault start'. A
	// binary that predates the command rejects the flag and exits non-zero,
	// whatever its help text says.
	supportCheckArgs = " vault start --check-support"

	// unitPATH is the PATH the unit runs ocfp with. Non-interactive
	// sessions on bastions have dropped linuxbrew's bin directory before,
	// and safe, tmux, and the engine are installed there.
	unitPATH = "/home/linuxbrew/.linuxbrew/bin:/home/linuxbrew/.linuxbrew/sbin:" +
		"/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

var (
	// ErrInvalidBloc reports a bloc name that cannot be used as the unit's
	// instance name as written.
	ErrInvalidBloc = errors.New("the bloc name cannot name an ocfp-vault unit instance")

	// ErrInvalidUnitParams reports a user, home, or ocfp path that would
	// break the unit file or run it as the wrong user.
	ErrInvalidUnitParams = errors.New("invalid ocfp-vault unit parameters")
)

var (
	// blocPattern keeps to the characters systemd takes literally in an
	// instance name. A dash is fine, because the unit uses %i, the
	// instance as written; a slash, a backslash, a space, or a percent
	// sign would be escaped or expanded.
	blocPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

	// userPattern is the portable shape of a Linux user name.
	userPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

	// pathPattern is an absolute path with no character a unit file would
	// split on, quote, or expand.
	pathPattern = regexp.MustCompile(`^/[A-Za-z0-9_./+-]*$`)
)

// UnitParams are the bastion-specific values the unit is rendered with.
type UnitParams struct {
	// User is the operator user the vault runs as. Its key files live
	// under its home, and safe's targets live in its ~/.saferc.
	User string
	// Home is that user's home directory.
	Home string
	// OCFPPath is the absolute path of the ocfp binary.
	OCFPPath string
}

// Unit is the rendered unit, as the two files that bastion init installs.
type Unit struct {
	// Template is the text of ocfp-vault@.service.
	Template string
	// DropIn is the text of the bloc instance's operator.conf drop-in.
	DropIn string
}

// unitTemplate is the ocfp-vault@.service text. The placeholder is filled
// by Render with a value it has validated.
const unitTemplate = `# Managed by OCFP. 'ocfp init bastion' writes this file and overwrites it
# on every run.
#
# Each instance, ocfp-vault@<bloc>.service, brings that bloc's inception
# vault back after a reboot by running 'ocfp vault start'. That command only
# reopens the vault the bloc already has. It never archives a vault and never
# starts a new one, and when anything is missing it refuses and exits
# non-zero, so this unit is safe to leave enabled after a teardown.
#
# The user an instance runs as, and the key files it waits for, are set in
# that instance's drop-in, ocfp-vault@<bloc>.service.d/operator.conf, which
# 'ocfp init bastion' writes next to this file. An instance without that
# drop-in never starts, so this template never runs the vault as root.
[Unit]
Description=OCFP inception vault for bloc %i
After=ocfp-dataset.service network-online.target
Wants=network-online.target
ConditionPathExists=/etc/systemd/system/ocfp-vault@%i.service.d/operator.conf

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart={{OCFP}} vault start --bloc %i
# A failed start is left for an operator to read, never retried.
Restart=no
TimeoutStartSec=10min
# The vault runs in a tmux session that outlives this oneshot. Stopping or
# restarting the unit must not kill it, or any other tmux session that
# shares its server; 'ocfp vault teardown' is how the vault is stopped.
KillMode=process

[Install]
WantedBy=multi-user.target
`

// dropInTemplate is the text of an instance's operator.conf drop-in. The
// placeholders are filled by Render with values it has validated.
const dropInTemplate = `# Managed by OCFP. 'ocfp init bastion' writes this file and overwrites it
# on every run. It names the operator that this bloc's ocfp-vault instance
# runs as, because the bloc's key files and safe's targets live under that
# operator's home.
[Unit]
# The instance starts only when the bloc has an unseal key, in either the
# current layout or the legacy ~/.ocfp one.
ConditionPathExists=|{{HOME}}/.local/share/ocfp/%i/vault/unseal.keys
ConditionPathExists=|{{HOME}}/.ocfp/%i/vault/unseal.keys

[Service]
User={{USER}}
WorkingDirectory={{HOME}}
Environment=HOME={{HOME}}
Environment=PATH={{PATH}}
`

// Render returns the template and the drop-in for one bastion's operator.
func Render(params UnitParams) (Unit, error) {
	err := params.validate()
	if err != nil {
		return Unit{}, err
	}

	replacer := strings.NewReplacer(
		"{{HOME}}", params.Home,
		"{{USER}}", params.User,
		"{{PATH}}", unitPATH,
		"{{OCFP}}", params.OCFPPath,
	)

	return Unit{
		Template: replacer.Replace(unitTemplate),
		DropIn:   replacer.Replace(dropInTemplate),
	}, nil
}

// validate refuses values that would break the unit file, or run the vault
// as root, whose home holds none of the operator's keys.
func (p UnitParams) validate() error {
	switch {
	case !userPattern.MatchString(p.User) || p.User == "root":
		return fmt.Errorf("%w: user %q must be the operator's own user name", ErrInvalidUnitParams, p.User)
	case !pathPattern.MatchString(p.Home) || filepath.Clean(p.Home) == "/":
		return fmt.Errorf("%w: home %q must be an absolute path with no spaces or special characters",
			ErrInvalidUnitParams, p.Home)
	case !pathPattern.MatchString(p.OCFPPath):
		return fmt.Errorf("%w: ocfp path %q must be an absolute path with no spaces or special characters",
			ErrInvalidUnitParams, p.OCFPPath)
	}

	return nil
}

// UnitName returns the instance of the unit that serves bloc.
func UnitName(bloc string) (string, error) {
	if !blocPattern.MatchString(bloc) {
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

// SupportCheckCommand returns the command that exits zero only when the
// ocfp binary at ocfpPath has 'vault start', and changes nothing.
func SupportCheckCommand(ocfpPath string) string {
	return ocfpPath + supportCheckArgs
}

// installScript writes the unit's two files. It decodes them into a
// temporary directory first, under the instance's own name so that
// systemd-analyze reads the drop-in with it, and verifies them when
// systemd-analyze is there. Only then does it install them, so a bad
// decode or a unit that fails to verify leaves the installed unit as it
// was. --recursive-errors=no keeps verify to this unit, where the systemd
// version has that flag, because another unit's problems are not ours.
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
sudo install -m 0644 "$tmp/{{NAME}}" {{UNIT_PATH}}
sudo install -d -m 0755 {{DROPIN_DIR}}
sudo install -m 0644 "$tmp/{{NAME}}.d/{{DROPIN_NAME}}" {{DROPIN_PATH}}
sudo systemctl daemon-reload
sudo systemctl enable {{NAME}}`

// InstallCommand returns the shell command that installs unit's template at
// UnitPath and its drop-in at DropInPath(bloc), reloads systemd, and enables
// bloc's instance. It enables the instance without starting it, so nothing
// runs until the next boot. The script holds no single quote, so it is
// safe inside bash -c '...'.
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
	).Replace(installScript)

	return "bash -c '" + script + "'", nil
}
