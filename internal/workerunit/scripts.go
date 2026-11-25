package workerunit

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/fivetwenty-io/ocfp-cli/internal/ownerrecord"
)

// The scripts below run on the bastion through the operator's login shell,
// each as one bash -c '...' command, so none of them may hold a single
// quote. Only the steps that must be root run under sudo: the binary's
// install, the directory's creation, and the signing with the host key.
const (
	// AttestationNamespace is the SSHSIG namespace the bastion's host key
	// signs the join request's attestation under.
	AttestationNamespace = "ocfp-bastion-join-v1"

	// HostKeyPath is the bastion's ed25519 host key, which signs the
	// attestation.
	HostKeyPath = "/etc/ssh/ssh_host_ed25519_key"

	// RemoteOCFP is the ocfp binary the worker runs on its bastion.
	RemoteOCFP = "/usr/local/bin/ocfp"

	// loopbackHost and loopbackPort are where the worker reaches its own
	// bastion's sshd.
	loopbackHost = "127.0.0.1"
	loopbackPort = 22

	// sshDir, spoolDir, and the file names below are inside the worker's
	// directory.
	sshDir          = "ssh"
	spoolDir        = "spool"
	loopbackKey     = "ssh/id_ed25519"
	knownHostsFile  = "ssh/known_hosts"
	recordFile      = "bastion.json"
	attestationFile = "attestation.json"
	signatureFile   = "attestation.sig"
	joinUpload      = ".join.json.upload"
	binaryUpload    = ".bloc-worker.upload"

	// binaryStaging is where the new binary waits beside the installed one,
	// so the swap is a rename and a running worker is never written over.
	binaryStaging = "/usr/local/bin/.bloc-worker.new"

	// ed25519Type is the key type the host key must have.
	ed25519Type = "ssh-ed25519"

	// fingerprintTail is how many of the fingerprint's last characters the
	// operator types to approve a bloc with no host key on record.
	fingerprintTail = 12
)

var (
	// ErrProbe reports a bastion probe that did not report a value the
	// install needs.
	ErrProbe = errors.New("the bastion probe did not report what the worker install needs")

	// ErrNoHostKey reports a bastion with no usable ed25519 host key, which
	// the attestation must be signed with.
	ErrNoHostKey = errors.New("the bastion has no ed25519 host key at /etc/ssh/ssh_host_ed25519_key.pub")

	// errQuote reports a script that would break out of bash -c '...'.
	errQuote = errors.New("a bastion script holds a single quote")
)

// Probe is what the bastion reports before the worker is installed.
type Probe struct {
	// User and Group are the operator the worker runs as.
	User  string
	Group string
	// Home is the operator's home directory.
	Home string
	// Arch is what uname -m prints.
	Arch string
	// BinarySHA256 is the installed worker binary's SHA256, or empty when
	// none is installed.
	BinarySHA256 string
	// JoinSHA256 is the installed join file's SHA256, or empty when none is
	// installed.
	JoinSHA256 string
	// HostKey is the ed25519 host public key, in authorized_keys form.
	HostKey string
}

// Record is the bastion record the worker reads to reach its own bastion.
// Its fields are exactly the ones the worker's record loader accepts, and
// it refuses any other.
type Record struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	KeyPath    string `json:"key_path"`
	KnownHosts string `json:"known_hosts"`
	RemoteOCFP string `json:"remote_ocfp"`
	OwnerDir   string `json:"owner_dir"`
	SpoolRoot  string `json:"spool_root"`
}

const probeScript = `echo "user=$(id -un)"
echo "group=$(id -gn)"
echo "home=$HOME"
echo "arch=$(uname -m)"
binary_sha256=""
if [ -f {{BINARY}} ]; then binary_sha256="$(sha256sum {{BINARY}} | cut -d" " -f1)"; fi
echo "binary_sha256=$binary_sha256"
join_sha256=""
if [ -r {{DIR}}/join.json ]; then join_sha256="$(sha256sum {{DIR}}/join.json | cut -d" " -f1)"; fi
echo "join_sha256=$join_sha256"
host_key=""
if [ -r {{HOST_KEY}}.pub ]; then host_key="$(head -n 1 {{HOST_KEY}}.pub)"; fi
echo "host_key=$host_key"`

const directoryScript = `sudo install -d -m 0755 -o root -g root {{ROOT}}
sudo install -d -m 0700 -o {{USER}} -g {{GROUP}} {{DIR}} {{DIR}}/{{SSH}} {{DIR}}/{{SPOOL}}`

const joinFileScript = `echo "{{SHA}}  {{UPLOAD}}" | sha256sum -c --quiet -
chmod 0600 {{UPLOAD}}
mv -f {{UPLOAD}} {{DIR}}/join.json
echo "{{MARKER}} join file"`

const binaryScript = `echo "{{SHA}}  {{UPLOAD}}" | sha256sum -c --quiet -
sudo install -o root -g root -m 0755 {{UPLOAD}} {{STAGING}}
sudo mv -f {{STAGING}} {{BINARY}}
rm -f {{UPLOAD}}
echo "{{MARKER}} binary"`

// prepareScript has the worker write its key and the attestation, and
// signs the attestation only when it is new or changed, so a rerun that
// changes nothing keeps the request id the worker already sent.
const prepareScript = `before=""
if [ -f {{ATT}} ]; then before="$(sha256sum {{ATT}} | cut -d" " -f1)"; fi
{{BINARY}} join prepare --dir {{DIR}}
after="$(sha256sum {{ATT}} | cut -d" " -f1)"
if [ "$before" != "$after" ] || [ ! -s {{SIG}} ]; then
  sudo ssh-keygen -Y sign -f {{HOST_KEY}} -n {{NAMESPACE}} < {{ATT}} > {{SIG}}.tmp
  chmod 0600 {{SIG}}.tmp
  mv -f {{SIG}}.tmp {{SIG}}
  echo "{{MARKER}} attestation"
fi`

// loopbackScript gives the worker a key that only reaches this bastion's
// own sshd, authorizes it for the operator from the loopback addresses
// alone, and pins this bastion's host keys for 127.0.0.1.
const loopbackScript = `if [ ! -f {{KEY}} ]; then
  ssh-keygen -q -t ed25519 -N "" -C {{COMMENT}} -f {{KEY}}
  echo "{{MARKER}} loopback key"
fi
if [ ! -s {{KEY}}.pub ]; then ssh-keygen -y -f {{KEY}} > {{KEY}}.pub; fi
line="from=\"127.0.0.1,::1\",no-agent-forwarding,no-port-forwarding,no-X11-forwarding $(cut -d" " -f1,2 {{KEY}}.pub) {{COMMENT}}"
if [ ! -d "$HOME/.ssh" ]; then mkdir -m 0700 "$HOME/.ssh"; fi
if [ ! -e "$HOME/.ssh/authorized_keys" ]; then install -m 0600 /dev/null "$HOME/.ssh/authorized_keys"; fi
if ! grep -qxF -- "$line" "$HOME/.ssh/authorized_keys"; then
  tmp="$(mktemp "$HOME/.ssh/authorized_keys.XXXXXX")"
  awk -v c={{COMMENT}} "\$NF != c" "$HOME/.ssh/authorized_keys" > "$tmp"
  printf "%s\n" "$line" >> "$tmp"
  chmod 0600 "$tmp"
  mv -f "$tmp" "$HOME/.ssh/authorized_keys"
  echo "{{MARKER}} loopback authorization"
fi
known="$(for f in /etc/ssh/ssh_host_*_key.pub; do printf "{{LOOPBACK}} %s\n" "$(cut -d" " -f1,2 "$f")"; done)"
if [ "$(cat {{KNOWN}} 2>/dev/null || true)" != "$known" ]; then
  printf "%s\n" "$known" > {{KNOWN}}.tmp
  chmod 0644 {{KNOWN}}.tmp
  mv -f {{KNOWN}}.tmp {{KNOWN}}
  echo "{{MARKER}} loopback known hosts"
fi`

const recordScript = `echo {{RECORD}} | base64 -d > {{FILE}}.tmp
chmod 0600 {{FILE}}.tmp
if cmp -s {{FILE}}.tmp {{FILE}}; then
  rm -f {{FILE}}.tmp
else
  mv -f {{FILE}}.tmp {{FILE}}
  echo "{{MARKER}} record"
fi`

// bash wraps script into one bash -c '...' command that stops at the
// first failure.
func bash(script string, pairs ...string) (string, error) {
	body := "set -euo pipefail\n" + strings.NewReplacer(pairs...).Replace(script)
	if strings.Contains(body, "'") {
		return "", errQuote
	}

	return "bash -c '" + body + "'", nil
}

// ProbeCommand returns the command that reports, one key=value line each,
// the operator's user, group, and home, the machine, the sums of the
// installed worker binary and join file, and the ed25519 host public key.
func ProbeCommand(bloc string) (string, error) {
	dir, err := Dir(bloc)
	if err != nil {
		return "", err
	}

	return bash(probeScript, "{{BINARY}}", BinaryPath, "{{DIR}}", dir, "{{HOST_KEY}}", HostKeyPath)
}

// ParseProbe reads what ProbeCommand printed. A login banner may come
// first, so each key's last line wins.
func ParseProbe(out string) (Probe, error) {
	values := map[string]string{}

	for line := range strings.SplitSeq(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimRight(line, "\r"), "=")
		if ok {
			values[key] = strings.TrimSpace(value)
		}
	}

	probe := Probe{
		User:         values["user"],
		Group:        values["group"],
		Home:         values["home"],
		Arch:         values["arch"],
		BinarySHA256: values["binary_sha256"],
		JoinSHA256:   values["join_sha256"],
		HostKey:      values["host_key"],
	}

	for key, value := range map[string]string{
		"user": probe.User, "group": probe.Group, "home": probe.Home, "arch": probe.Arch,
	} {
		if value == "" {
			return Probe{}, fmt.Errorf("%w: no %s", ErrProbe, key)
		}
	}

	_, err := HostKeyFingerprint(probe.HostKey)
	if err != nil {
		return Probe{}, err
	}

	return probe, nil
}

// HostKeyFingerprint returns the SHA256 fingerprint of an ed25519 public
// key in authorized_keys form, as ssh-keygen -l prints it.
func HostKeyFingerprint(authorizedKey string) (string, error) {
	fields := strings.Fields(authorizedKey)
	if len(fields) < 2 || fields[0] != ed25519Type {
		return "", ErrNoHostKey
	}

	wire, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return "", ErrNoHostKey
	}

	key, err := ssh.ParsePublicKey(wire)
	if err != nil || key.Type() != ed25519Type {
		return "", ErrNoHostKey
	}

	return ssh.FingerprintSHA256(key), nil
}

// FingerprintTail returns the fingerprint's last characters, which the
// operator types in the workers panel to approve a bloc whose host key is
// not on record yet.
func FingerprintTail(fingerprint string) string {
	if len(fingerprint) <= fingerprintTail {
		return fingerprint
	}

	return fingerprint[len(fingerprint)-fingerprintTail:]
}

// DirectoryCommand returns the command that creates DataRoot owned by root
// and bloc's worker directory, with its ssh and spool directories, owned
// by the operator with mode 0700.
func DirectoryCommand(bloc string, probe Probe) (string, error) {
	dir, err := Dir(bloc)
	if err != nil {
		return "", err
	}

	if !userPattern.MatchString(probe.User) || probe.User == "root" || !userPattern.MatchString(probe.Group) {
		return "", fmt.Errorf("%w: user %q and group %q must be the operator's own",
			ErrInvalidUnitParams, probe.User, probe.Group)
	}

	return bash(directoryScript,
		"{{ROOT}}", DataRoot, "{{USER}}", probe.User, "{{GROUP}}", probe.Group,
		"{{DIR}}", dir, "{{SSH}}", sshDir, "{{SPOOL}}", spoolDir)
}

// JoinUploadPath returns where the join file is uploaded before it is
// checked and moved into place.
func JoinUploadPath(bloc string) (string, error) {
	return inDir(bloc, joinUpload)
}

// BinaryUploadPath returns where the worker binary is uploaded before it
// is checked and installed.
func BinaryUploadPath(bloc string) (string, error) {
	return inDir(bloc, binaryUpload)
}

// JoinFileCommand returns the command that checks the uploaded join file
// against sha and moves it into place with mode 0600.
func JoinFileCommand(bloc, sha string) (string, error) {
	dir, upload, err := uploadPaths(bloc, joinUpload, sha)
	if err != nil {
		return "", err
	}

	return bash(joinFileScript, "{{SHA}}", sha, "{{UPLOAD}}", upload, "{{DIR}}", dir, "{{MARKER}}", ChangeMarker)
}

// BinaryCommand returns the command that checks the uploaded worker binary
// against sha and installs it at BinaryPath, owned by root with mode 0755.
func BinaryCommand(bloc, sha string) (string, error) {
	_, upload, err := uploadPaths(bloc, binaryUpload, sha)
	if err != nil {
		return "", err
	}

	return bash(binaryScript,
		"{{SHA}}", sha, "{{UPLOAD}}", upload, "{{STAGING}}", binaryStaging,
		"{{BINARY}}", BinaryPath, "{{MARKER}}", ChangeMarker)
}

// PrepareCommand returns the command that has the worker write its key and
// the attestation, then signs the attestation with the bastion's host key
// under sudo when it is new or changed.
func PrepareCommand(bloc string) (string, error) {
	dir, err := Dir(bloc)
	if err != nil {
		return "", err
	}

	return bash(prepareScript,
		"{{ATT}}", dir+"/"+attestationFile, "{{SIG}}", dir+"/"+signatureFile,
		"{{BINARY}}", BinaryPath, "{{DIR}}", dir, "{{HOST_KEY}}", HostKeyPath,
		"{{NAMESPACE}}", AttestationNamespace, "{{MARKER}}", ChangeMarker)
}

// LoopbackCommand returns the command that makes the worker's loopback
// key, authorizes it for the operator from 127.0.0.1 and ::1 only, and
// writes the known_hosts file that pins this bastion's host keys.
func LoopbackCommand(bloc string) (string, error) {
	dir, err := Dir(bloc)
	if err != nil {
		return "", err
	}

	return bash(loopbackScript,
		"{{KEY}}", dir+"/"+loopbackKey, "{{KNOWN}}", dir+"/"+knownHostsFile,
		"{{COMMENT}}", BundleBinary+"@"+bloc, "{{LOOPBACK}}", loopbackHost, "{{MARKER}}", ChangeMarker)
}

// NewRecord returns bloc's bastion record for the operator user.
func NewRecord(bloc, user string) (Record, error) {
	dir, err := Dir(bloc)
	if err != nil {
		return Record{}, err
	}

	if !userPattern.MatchString(user) || user == "root" {
		return Record{}, fmt.Errorf("%w: user %q must be the operator's own user name", ErrInvalidUnitParams, user)
	}

	return Record{
		Host:       loopbackHost,
		Port:       loopbackPort,
		User:       user,
		KeyPath:    dir + "/" + loopbackKey,
		KnownHosts: dir + "/" + knownHostsFile,
		RemoteOCFP: RemoteOCFP,
		OwnerDir:   ownerrecord.DefaultDir,
		SpoolRoot:  dir + "/" + spoolDir,
	}, nil
}

// RecordCommand returns the command that writes record as bloc's
// bastion.json with mode 0600, and reports a change only when it differs.
func RecordCommand(bloc string, record Record) (string, error) {
	file, err := inDir(bloc, recordFile)
	if err != nil {
		return "", err
	}

	body, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encoding the bastion record: %w", err)
	}

	return bash(recordScript,
		"{{RECORD}}", base64.StdEncoding.EncodeToString(append(body, '\n')),
		"{{FILE}}", file, "{{MARKER}}", ChangeMarker)
}

// Changed reports whether a script's output holds a change line.
func Changed(out string) bool {
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), ChangeMarker) {
			return true
		}
	}

	return false
}

// inDir returns name inside bloc's worker directory.
func inDir(bloc, name string) (string, error) {
	dir, err := Dir(bloc)
	if err != nil {
		return "", err
	}

	return dir + "/" + name, nil
}

// uploadPaths returns bloc's directory and its upload path for name, and
// refuses a sum that is not a lowercase hex SHA256.
func uploadPaths(bloc, name, sha string) (string, string, error) {
	if !sumForm.MatchString(sha) {
		return "", "", fmt.Errorf("%w: %q is not a lowercase hex SHA256", ErrBundleSum, sha)
	}

	dir, err := Dir(bloc)
	if err != nil {
		return "", "", err
	}

	return dir, dir + "/" + name, nil
}
