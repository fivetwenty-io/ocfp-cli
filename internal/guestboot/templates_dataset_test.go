package guestboot

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A thin pool gets deleted blocks back only when the guest sends TRIM, so the
// data disk mounts with discard both on the first mount and from fstab. The
// bind mounts on top of it carry no discard, because it means nothing there.
func TestDatasetScriptMountsDataDiskWithDiscard(t *testing.T) {
	for _, want := range []string{
		`mount -o discard "${REAL}" "${MOUNT}"`,
		`printf 'UUID=%s %s %s defaults,nofail,discard 0 2\n'`,
	} {
		if !strings.Contains(DatasetScript, want) {
			t.Errorf("dataset script is missing %q", want)
		}
	}

	if got := strings.Count(DatasetScript, "none bind,nofail 0 0"); got != 2 {
		t.Errorf("dataset script has %d bind fstab lines without discard, want 2", got)
	}

	if strings.Contains(DatasetScript, "bind,nofail,discard") {
		t.Error("dataset script adds discard to a bind mount")
	}
}

// cloud-init's init stage runs the ssh module, so the dataset unit has to
// finish first. Ubuntu Resolute renamed that stage's unit from
// cloud-init.service to cloud-init-network.service, and ordering against a
// unit that does not exist orders against nothing. Both names stay, because
// Ubuntu Noble still ships the old one.
func TestDatasetServiceOrdersBeforeBothCloudInitNetworkUnits(t *testing.T) {
	before := map[string]bool{}

	for _, line := range strings.Split(DatasetService, "\n") {
		if names, ok := strings.CutPrefix(line, "Before="); ok {
			for _, name := range strings.Fields(names) {
				before[name] = true
			}
		}
	}

	for _, want := range []string{"cloud-init.service", "cloud-init-network.service", "ssh.service", "sshd.service", "tailscaled.service", "ocfp-firstboot.service", "sysinit.target"} {
		if !before[want] {
			t.Errorf("ocfp-dataset.service is not ordered before %s:\n%s", want, DatasetService)
		}
	}
}

// datasetPathLine is the PATH the dataset script runs with. systemd starts it
// with a minimal PATH, and a bastion whose apt jq has been removed may still
// have one under /usr/local/bin or linuxbrew. Those directories come after
// the system ones, so they only ever supply a tool the system lacks.
const datasetPathLine = `export PATH="${PATH:-/usr/sbin:/usr/bin:/sbin:/bin}:/usr/local/sbin:/usr/local/bin:/home/linuxbrew/.linuxbrew/bin"`

func TestDatasetScriptExtendsPathForJQ(t *testing.T) {
	if !strings.Contains(DatasetScript, "\n"+datasetPathLine+"\n") {
		t.Fatalf("dataset script is missing the PATH line %q", datasetPathLine)
	}

	if strings.Index(DatasetScript, datasetPathLine) > strings.Index(DatasetScript, "dmidecode") {
		t.Error("the dataset script sets PATH after it first runs a tool")
	}
}

// runDatasetWithoutJQ runs script with a PATH that holds only stubs for
// dmidecode and logger and a link to cat, so jq cannot be found however the test host is set
// up. The SKU stub carries a data block, the case that used to be reported as
// having none.
func runDatasetWithoutJQ(t *testing.T, script string) (string, error) {
	t.Helper()

	return runDatasetWithJQStub(t, script, "")
}

// runDatasetWithJQStub is runDatasetWithoutJQ with a jq stub on the PATH when
// jqBody is not empty. The stub is the whole script after its shebang.
func runDatasetWithJQStub(t *testing.T, script, jqBody string) (string, error) {
	t.Helper()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}

	dir := t.TempDir()
	sku := filepath.Join(dir, "guest.json")

	for name, body := range map[string]string{
		"dmidecode": "#!" + bash + "\nprintf '%s' '{\"data\":{\"serial\":\"stub-serial\"}}'\n",
		"logger":    "#!" + bash + "\nshift 4\nprintf '%s\\n' \"$*\" >&2\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	if jqBody != "" {
		if err := os.WriteFile(filepath.Join(dir, "jq"), []byte("#!"+bash+"\n"+jqBody+"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(sku, []byte(`{"data":{"serial":"stub-serial"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// The cloud variant reads its SKU with cat.
	if cat, err := exec.LookPath("cat"); err == nil {
		if err := os.Symlink(cat, filepath.Join(dir, "cat")); err != nil {
			t.Fatal(err)
		}
	}

	if !strings.Contains(script, datasetPathLine) {
		t.Fatal("the script has no PATH line to replace")
	}

	script = strings.Replace(script, datasetPathLine, "export PATH="+dir, 1)
	script = strings.ReplaceAll(script, "/etc/ocfp/guest.json", sku)

	cmd := exec.Command(bash)
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = []string{"PATH=" + dir}
	out, err := cmd.CombinedOutput()

	return string(out), err
}

// With jq gone, the script used to turn every lookup into an empty answer and
// report that the SKU had no data block, which hid the real fault and skipped
// the host-key restore. It now names jq and fails, so the journal and
// systemctl both show what happened. The output never carries the SKU, which
// on PVE sits next to a tailscale auth key.
func TestDatasetScriptFailsLoudlyWithoutJQ(t *testing.T) {
	out, err := runDatasetWithoutJQ(t, DatasetScript)

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("dataset script without jq: err = %v, want exit status 1; output:\n%s", err, out)
	}

	if !strings.Contains(out, "FATAL: jq not found") {
		t.Errorf("dataset script without jq does not name jq:\n%s", out)
	}

	if strings.Contains(out, "nothing to do") {
		t.Errorf("dataset script without jq still reports nothing to do:\n%s", out)
	}

	if strings.Contains(out, "stub-serial") {
		t.Errorf("dataset script output carries the SKU:\n%s", out)
	}
}

// A jq that is on the PATH but cannot run, for example because a library is
// missing, used to read as a SKU with no data block, and the script reported
// that there was nothing to do. It now says jq failed and with what status,
// and the output still never carries the SKU.
func TestDatasetScriptFailsLoudlyWhenJQCannotRun(t *testing.T) {
	out, err := runDatasetWithJQStub(t, DatasetScript, "exit 127")

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("dataset script with a broken jq: err = %v, want exit status 1; output:\n%s", err, out)
	}

	if !strings.Contains(out, "FATAL: jq failed with exit status 127") {
		t.Errorf("dataset script does not name the jq failure:\n%s", out)
	}

	if strings.Contains(out, "nothing to do") {
		t.Errorf("dataset script reports nothing to do for a broken jq:\n%s", out)
	}

	if strings.Contains(out, "stub-serial") {
		t.Errorf("dataset script output carries the SKU:\n%s", out)
	}
}
