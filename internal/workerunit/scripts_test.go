package workerunit_test

import (
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fivetwenty-io/ocfp-cli/internal/workerunit"
)

const testDir = "/data/bloc-worker/ocfp-lab-drgao"

const testHostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl root@bastion"

// assertBashScript checks that cmd is one bash -c '...' script that the
// operator's login shell runs whole.
func assertBashScript(t *testing.T, cmd string) {
	t.Helper()

	require.True(t, strings.HasPrefix(cmd, "bash -c '"), cmd)
	require.True(t, strings.HasSuffix(cmd, "'"), cmd)

	body := strings.TrimSuffix(strings.TrimPrefix(cmd, "bash -c '"), "'")
	assert.NotContains(t, body, "'", "the script holds no single quote")
	assert.Contains(t, body, "set -euo pipefail")
}

func TestProbeCommand(t *testing.T) {
	t.Parallel()

	cmd, err := workerunit.ProbeCommand(testBloc)
	require.NoError(t, err)
	assertBashScript(t, cmd)

	for _, want := range []string{
		"id -un", "id -gn", "uname -m",
		"sha256sum " + workerunit.BinaryPath,
		"sha256sum " + testDir + "/join.json",
		"/etc/ssh/ssh_host_ed25519_key.pub",
	} {
		assert.Contains(t, cmd, want)
	}

	assert.NotContains(t, cmd, "sudo", "the probe needs no privilege")

	_, err = workerunit.ProbeCommand("bad/bloc")
	require.ErrorIs(t, err, workerunit.ErrInvalidBloc)
}

func TestParseProbe(t *testing.T) {
	t.Parallel()

	out := strings.Join([]string{
		"Welcome to the bastion",
		"user=ubuntu",
		"group=ubuntu",
		"home=/home/ubuntu",
		"arch=x86_64",
		"binary_sha256=" + strings.Repeat("ab", 32),
		"join_sha256=",
		"host_key=" + testHostKey,
		"",
	}, "\n")

	probe, err := workerunit.ParseProbe(out)
	require.NoError(t, err)

	assert.Equal(t, workerunit.Probe{
		User:         "ubuntu",
		Group:        "ubuntu",
		Home:         "/home/ubuntu",
		Arch:         "x86_64",
		BinarySHA256: strings.Repeat("ab", 32),
		JoinSHA256:   "",
		HostKey:      testHostKey,
	}, probe)
}

func TestParseProbe_Refusals(t *testing.T) {
	t.Parallel()

	full := map[string]string{
		"user": "ubuntu", "group": "ubuntu", "home": "/home/ubuntu", "arch": "x86_64",
		"binary_sha256": "", "join_sha256": "", "host_key": testHostKey,
	}

	render := func(drop string) string {
		var lines []string

		for k, v := range full {
			if k != drop {
				lines = append(lines, k+"="+v)
			}
		}

		return strings.Join(lines, "\n")
	}

	for _, key := range []string{"user", "group", "home", "arch"} {
		_, err := workerunit.ParseProbe(render(key))
		require.ErrorIs(t, err, workerunit.ErrProbe, key)
	}

	_, err := workerunit.ParseProbe(render("host_key"))
	require.ErrorIs(t, err, workerunit.ErrNoHostKey)

	_, err = workerunit.ParseProbe(strings.Replace(render(""), "host_key="+testHostKey, "host_key=ssh-rsa AAAAB3Nza", 1))
	require.ErrorIs(t, err, workerunit.ErrNoHostKey)
}

func TestHostKeyFingerprint(t *testing.T) {
	t.Parallel()

	fp, err := workerunit.HostKeyFingerprint(testHostKey)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(fp, "SHA256:"), fp)
	assert.Len(t, fp, len("SHA256:")+43)

	assert.Equal(t, fp[len(fp)-12:], workerunit.FingerprintTail(fp))

	_, err = workerunit.HostKeyFingerprint("ssh-ed25519 not-base64")
	require.ErrorIs(t, err, workerunit.ErrNoHostKey)
}

func TestDirectoryCommand(t *testing.T) {
	t.Parallel()

	cmd, err := workerunit.DirectoryCommand(testBloc, workerunit.Probe{User: "ubuntu", Group: "ops"})
	require.NoError(t, err)
	assertBashScript(t, cmd)

	assert.Contains(t, cmd, "sudo install -d -m 0755 -o root -g root /data/bloc-worker")
	assert.Contains(t, cmd, "sudo install -d -m 0700 -o ubuntu -g ops "+testDir+" "+testDir+"/ssh "+testDir+"/spool")

	for name, probe := range map[string]workerunit.Probe{
		"root":      {User: "root", Group: "root"},
		"odd group": {User: "ubuntu", Group: "a b"},
	} {
		_, err := workerunit.DirectoryCommand(testBloc, probe)
		require.ErrorIs(t, err, workerunit.ErrInvalidUnitParams, name)
	}
}

func TestJoinFileCommand(t *testing.T) {
	t.Parallel()

	sha := strings.Repeat("0f", 32)

	cmd, err := workerunit.JoinFileCommand(testBloc, sha)
	require.NoError(t, err)
	assertBashScript(t, cmd)

	upload, err := workerunit.JoinUploadPath(testBloc)
	require.NoError(t, err)
	assert.Equal(t, testDir+"/.join.json.upload", upload)

	assert.Contains(t, cmd, "echo \""+sha+"  "+upload+"\" | sha256sum -c --quiet -")
	assert.Contains(t, cmd, "chmod 0600 "+upload)
	assert.Contains(t, cmd, "mv -f "+upload+" "+testDir+"/join.json")
	assert.Contains(t, cmd, workerunit.ChangeMarker+" join file")
	assert.NotContains(t, cmd, "sudo", "the operator owns the worker directory")

	_, err = workerunit.JoinFileCommand(testBloc, "not-a-sum")
	require.ErrorIs(t, err, workerunit.ErrBundleSum)
}

func TestBinaryCommand(t *testing.T) {
	t.Parallel()

	sha := strings.Repeat("1e", 32)

	cmd, err := workerunit.BinaryCommand(testBloc, sha)
	require.NoError(t, err)
	assertBashScript(t, cmd)

	upload, err := workerunit.BinaryUploadPath(testBloc)
	require.NoError(t, err)
	assert.Equal(t, testDir+"/.bloc-worker.upload", upload)

	check := "echo \"" + sha + "  " + upload + "\" | sha256sum -c --quiet -"
	install := "sudo install -o root -g root -m 0755 " + upload + " /usr/local/bin/.bloc-worker.new"
	swap := "sudo mv -f /usr/local/bin/.bloc-worker.new /usr/local/bin/bloc-worker"

	for _, want := range []string{check, install, swap, "rm -f " + upload, workerunit.ChangeMarker + " binary"} {
		assert.Contains(t, cmd, want)
	}

	assert.Less(t, strings.Index(cmd, check), strings.Index(cmd, install), "the sum is checked on the bastion before install")
	assert.Less(t, strings.Index(cmd, install), strings.Index(cmd, swap))

	_, err = workerunit.BinaryCommand(testBloc, "")
	require.ErrorIs(t, err, workerunit.ErrBundleSum)
}

func TestPrepareCommand(t *testing.T) {
	t.Parallel()

	cmd, err := workerunit.PrepareCommand(testBloc)
	require.NoError(t, err)
	assertBashScript(t, cmd)

	prepare := "/usr/local/bin/bloc-worker join prepare --dir " + testDir
	sign := "sudo ssh-keygen -Y sign -f /etc/ssh/ssh_host_ed25519_key -n ocfp-bastion-join-v1 < " +
		testDir + "/attestation.json > " + testDir + "/attestation.sig.tmp"

	assert.Contains(t, cmd, prepare)
	assert.Contains(t, cmd, sign)
	assert.Contains(t, cmd, "mv -f "+testDir+"/attestation.sig.tmp "+testDir+"/attestation.sig")
	assert.Contains(t, cmd, workerunit.ChangeMarker+" attestation")
	assert.Less(t, strings.Index(cmd, prepare), strings.Index(cmd, sign), "the worker writes the attestation before it is signed")
	assert.Equal(t, 1, strings.Count(cmd, "sudo"), "only the signing runs as root")
	assert.Equal(t, "ocfp-bastion-join-v1", workerunit.AttestationNamespace)
}

func TestLoopbackCommand(t *testing.T) {
	t.Parallel()

	cmd, err := workerunit.LoopbackCommand(testBloc)
	require.NoError(t, err)
	assertBashScript(t, cmd)

	for _, want := range []string{
		"ssh-keygen -q -t ed25519 -N \"\" -C bloc-worker@ocfp-lab-drgao -f " + testDir + "/ssh/id_ed25519",
		"from=\\\"127.0.0.1,::1\\\",no-agent-forwarding,no-port-forwarding,no-X11-forwarding",
		"$HOME/.ssh/authorized_keys",
		"/etc/ssh/ssh_host_*_key.pub",
		testDir + "/ssh/known_hosts",
		"127.0.0.1",
		workerunit.ChangeMarker + " loopback",
	} {
		assert.Contains(t, cmd, want)
	}

	assert.NotContains(t, cmd, "sudo", "the loopback key and its authorization belong to the operator")
}

func TestRecord(t *testing.T) {
	t.Parallel()

	rec, err := workerunit.NewRecord(testBloc, "ubuntu")
	require.NoError(t, err)

	body, err := json.Marshal(rec)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(body, &got))

	assert.Equal(t, map[string]any{
		"host":        "127.0.0.1",
		"port":        float64(22),
		"user":        "ubuntu",
		"key_path":    testDir + "/ssh/id_ed25519",
		"known_hosts": testDir + "/ssh/known_hosts",
		"remote_ocfp": "/usr/local/bin/ocfp",
		"owner_dir":   "/data/ocfp-owner",
		"spool_root":  testDir + "/spool",
	}, got, "the record holds exactly the fields the worker's record loader accepts")

	_, err = workerunit.NewRecord(testBloc, "root")
	require.ErrorIs(t, err, workerunit.ErrInvalidUnitParams)
}

func TestRecordCommand(t *testing.T) {
	t.Parallel()

	rec, err := workerunit.NewRecord(testBloc, "ubuntu")
	require.NoError(t, err)

	cmd, err := workerunit.RecordCommand(testBloc, rec)
	require.NoError(t, err)
	assertBashScript(t, cmd)

	match := regexp.MustCompile(`echo ([A-Za-z0-9+/=]+) \| base64 -d > `).FindStringSubmatch(cmd)
	require.Len(t, match, 2)

	body, err := base64.StdEncoding.DecodeString(match[1])
	require.NoError(t, err)

	var back workerunit.Record
	require.NoError(t, json.Unmarshal(body, &back))
	assert.Equal(t, rec, back)

	assert.Contains(t, cmd, "mv -f "+testDir+"/bastion.json.tmp "+testDir+"/bastion.json")
	assert.Contains(t, cmd, workerunit.ChangeMarker+" record")
	assert.NotContains(t, cmd, "sudo")
}

func TestChanged(t *testing.T) {
	t.Parallel()

	assert.False(t, workerunit.Changed("all quiet\n"))
	assert.True(t, workerunit.Changed("noise\n"+workerunit.ChangeMarker+" binary\n"))
	assert.False(t, workerunit.Changed("echo "+workerunit.ChangeMarker+" in the middle"))
}

// TestScriptsParse has bash check every script's syntax, so a quoting
// slip fails here and not on a bastion.
func TestScriptsParse(t *testing.T) {
	t.Parallel()

	bashPath, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}

	rec, err := workerunit.NewRecord(testBloc, "ubuntu")
	require.NoError(t, err)

	sha := strings.Repeat("2d", 32)
	unit := renderTestUnit(t)

	build := map[string]func() (string, error){
		"probe": func() (string, error) { return workerunit.ProbeCommand(testBloc) },
		"directory": func() (string, error) {
			return workerunit.DirectoryCommand(testBloc, workerunit.Probe{User: "ubuntu", Group: "ubuntu"})
		},
		"join file": func() (string, error) { return workerunit.JoinFileCommand(testBloc, sha) },
		"binary":    func() (string, error) { return workerunit.BinaryCommand(testBloc, sha) },
		"prepare":   func() (string, error) { return workerunit.PrepareCommand(testBloc) },
		"loopback":  func() (string, error) { return workerunit.LoopbackCommand(testBloc) },
		"record":    func() (string, error) { return workerunit.RecordCommand(testBloc, rec) },
		"unit":      func() (string, error) { return workerunit.InstallCommand(testBloc, unit) },
	}

	for name, fn := range build {
		cmd, err := fn()
		require.NoError(t, err, name)

		body := strings.TrimSuffix(strings.TrimPrefix(cmd, "bash -c '"), "'")

		out, err := exec.CommandContext(t.Context(), bashPath, "-n", "-c", body).CombinedOutput()
		require.NoError(t, err, "%s: %s", name, out)
	}
}
