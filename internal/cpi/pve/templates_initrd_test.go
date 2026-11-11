package pve

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCatalog_DracutImagesDisableInitrdNetwork pins which catalog entries
// rebuild their initramfs without network modules. Resolute boots through
// dracut, whose initramfs DHCPs the NIC and leaves the link up, so a clone's
// cloud-init rename to eth0 fails and its static address never applies.
// Noble uses initramfs-tools and has no dracut to run, so the seed step
// would fail there.
func TestCatalog_DracutImagesDisableInitrdNetwork(t *testing.T) {
	t.Parallel()

	want := map[string]bool{
		"ubuntu-noble-template":            false,
		"ubuntu-noble-bastion-template":    false,
		"ubuntu-resolute-template":         true,
		"ubuntu-resolute-bastion-template": true,
	}

	for name, wantDisable := range want {
		spec, ok := LookupCatalogSpec(name)
		if !ok {
			t.Errorf("LookupCatalogSpec(%q) returned ok=false", name)

			continue
		}

		if spec.DisableInitrdNetwork != wantDisable {
			t.Errorf("%s DisableInitrdNetwork = %v, want %v", name, spec.DisableInitrdNetwork, wantDisable)
		}
	}
}

// TestTemplateSpec_NeedsSeed pins that the plain Resolute template is now
// seeded. Before the initramfs fix only bastion templates booted for a seed,
// so a plain template that skipped it would ship the DHCP initramfs.
func TestTemplateSpec_NeedsSeed(t *testing.T) {
	t.Parallel()

	want := map[string]bool{
		"ubuntu-noble-template":            false,
		"ubuntu-noble-bastion-template":    true,
		"ubuntu-resolute-template":         true,
		"ubuntu-resolute-bastion-template": true,
	}

	for name, wantSeed := range want {
		spec, ok := LookupCatalogSpec(name)
		if !ok {
			t.Errorf("LookupCatalogSpec(%q) returned ok=false", name)

			continue
		}

		if got := spec.needsSeed(); got != wantSeed {
			t.Errorf("%s needsSeed() = %v, want %v", name, got, wantSeed)
		}
	}
}

func TestInitrdNoNetworkConf_OmitsNetworkModules(t *testing.T) {
	t.Parallel()

	if !strings.HasPrefix(initrdNoNetworkConfPath, "/etc/dracut.conf.d/") || !strings.HasSuffix(initrdNoNetworkConfPath, ".conf") {
		t.Errorf("initrdNoNetworkConfPath = %q, want a .conf file under /etc/dracut.conf.d/, which is the only place dracut reads drop-ins from", initrdNoNetworkConfPath)
	}

	if !strings.Contains(initrdNoNetworkConf, "omit_dracutmodules+=") {
		t.Fatalf("initrdNoNetworkConf must append to omit_dracutmodules, not assign it, so it composes with other drop-ins")
	}

	for _, module := range []string{"net-lib", "systemd-networkd"} {
		if !strings.Contains(initrdNoNetworkConf, " "+module+" ") {
			t.Errorf("initrdNoNetworkConf must omit %q; every dracut network module depends on net-lib or systemd-networkd", module)
		}
	}
}

func TestInitrdNoNetworkScript_RebuildsAndVerifies(t *testing.T) {
	t.Parallel()

	for _, want := range []string{
		"set -euo pipefail",
		"dracut --quiet --force --regenerate-all --tmpdir",
		// The template disk is only as large as the cloud image, so
		// staging under /var/tmp runs out of space.
		"mount -t tmpfs",
		"lsinitrd -m",
		// An unreadable module list must fail, not pass as "no network
		// modules found".
		"grep -qx systemd",
		"net-lib|systemd-networkd",
	} {
		if !strings.Contains(initrdNoNetworkScript, want) {
			t.Errorf("initrdNoNetworkScript missing %q", want)
		}
	}
}

// TestInitrdNoNetworkScript_ParsesAsBash catches a quoting slip in the
// script before it reaches a template build, where it would only show up as
// a seed failure on the serial console.
func TestInitrdNoNetworkScript_ParsesAsBash(t *testing.T) {
	t.Parallel()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}

	cmd := exec.Command(bash, "-n")
	cmd.Stdin = strings.NewReader(initrdNoNetworkScript)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash -n rejected initrdNoNetworkScript: %v\n%s", err, out)
	}
}

// initrdRun is one execution of initrdNoNetworkScript under bash.
type initrdRun struct {
	// lsinitrd is what the fake lsinitrd prints for every image.
	lsinitrd string
	// availKiB is the free space the fake df reports for /boot.
	availKiB int
	// umountRC is the exit status of the fake umount.
	umountRC int
	// imageBytes is the size of the one initrd.img-* the run starts with.
	imageBytes int
}

// initrdModules renders the output of lsinitrd -m for a module list.
func initrdModules(modules ...string) string {
	return "Image: /boot/initrd.img-6.8.0\n========\ndracut modules:\n" + strings.Join(modules, "\n") + "\n"
}

// runInitrdScript runs initrdNoNetworkScript under bash with fake apt-get,
// dracut, lsinitrd, df, mktemp, mount, and umount first on PATH, so the
// rebuild and the verification run without root, a dracut install, or a real
// /boot. It returns the combined output, the log of the fake commands that
// ran in order, and the script's exit error (nil for exit 0).
func runInitrdScript(t *testing.T, run initrdRun) (string, string, error) {
	t.Helper()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	boot := filepath.Join(dir, "boot")
	stage := filepath.Join(dir, "stage")
	logPath := filepath.Join(dir, "calls.log")
	lsinitrdPath := filepath.Join(dir, "lsinitrd.out")

	for _, d := range []string{bin, boot} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	fakes := map[string]string{
		"apt-get":  "echo \"apt-get $*\" >> \"$OCFP_TEST_LOG\"\n",
		"dracut":   "echo \"dracut $*\" >> \"$OCFP_TEST_LOG\"\n",
		"lsinitrd": "cat \"$OCFP_TEST_LSINITRD\"\n",
		"mount":    "exit 0\n",
		"umount":   "exit \"$OCFP_TEST_UMOUNT_RC\"\n",
		"mktemp":   "mkdir -p \"$OCFP_TEST_STAGE\" && echo \"$OCFP_TEST_STAGE\"\n",
		"df": "echo 'Filesystem 1024-blocks Used Available Capacity Mounted on'\n" +
			"echo \"/dev/sda1 9999999 1 $OCFP_TEST_AVAIL_KIB 1% /boot\"\n",
	}

	for name, body := range fakes {
		writeFakeExecutableFile(t, filepath.Join(bin, name), "#!/bin/sh\n"+body)
	}

	if err := os.WriteFile(lsinitrdPath, []byte(run.lsinitrd), 0o600); err != nil {
		t.Fatalf("write lsinitrd output: %v", err)
	}

	if err := os.WriteFile(filepath.Join(boot, "initrd.img-6.8.0"), make([]byte, run.imageBytes), 0o600); err != nil {
		t.Fatalf("write initramfs image: %v", err)
	}

	scriptPath := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(scriptPath, []byte(initrdNoNetworkScript), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}

	cmd := exec.Command(bash, scriptPath)
	cmd.Env = append(os.Environ(),
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"OCFP_BOOT_DIR="+boot,
		"OCFP_TEST_LOG="+logPath,
		"OCFP_TEST_LSINITRD="+lsinitrdPath,
		"OCFP_TEST_STAGE="+stage,
		"OCFP_TEST_AVAIL_KIB="+strconv.Itoa(run.availKiB),
		"OCFP_TEST_UMOUNT_RC="+strconv.Itoa(run.umountRC),
	)

	out, runErr := cmd.CombinedOutput()
	calls, _ := os.ReadFile(logPath) //nolint:errcheck // a missing log means no fake ran, which the assertions report

	return string(out), string(calls), runErr
}

func defaultInitrdRun(lsinitrd string) initrdRun {
	return initrdRun{lsinitrd: lsinitrd, availKiB: 1_000_000, umountRC: 0, imageBytes: 1 << 20}
}

// TestInitrdNoNetworkScript_VerifiesModuleList runs the script's verification
// against the three module lists that matter. A clean list passes, a list
// that carries systemd-networkd fails, and an empty list fails rather than
// passing as "no network modules found".
func TestInitrdNoNetworkScript_VerifiesModuleList(t *testing.T) {
	if testing.Short() {
		t.Skip("covered by TestInitrdNoNetworkScript_VerifiesModuleList_Fast")
	}

	t.Parallel()

	tests := []struct {
		name     string
		lsinitrd string
		wantFail bool
		wantMsg  string
	}{
		{
			name:     "clean module list passes",
			lsinitrd: initrdModules("systemd", "systemd-sysusers", "base", "rootfs-block", "udev-rules"),
		},
		{
			name:     "systemd-networkd fails",
			lsinitrd: initrdModules("systemd", "systemd-networkd", "base"),
			wantFail: true,
			wantMsg:  "still carries network modules",
		},
		{
			name:     "empty module list fails",
			lsinitrd: initrdModules(),
			wantFail: true,
			wantMsg:  "cannot read the module list",
		},
		{
			name:     "no lsinitrd output fails",
			lsinitrd: "",
			wantFail: true,
			wantMsg:  "cannot read the module list",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, calls, err := runInitrdScript(t, defaultInitrdRun(tc.lsinitrd))

			if !tc.wantFail {
				if err != nil {
					t.Fatalf("script failed on a clean module list: %v\n%s", err, out)
				}

				if !strings.Contains(calls, "dracut --quiet --force --regenerate-all --tmpdir ") {
					t.Errorf("dracut was not run with --regenerate-all and a private --tmpdir; calls:\n%s", calls)
				}

				return
			}

			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
				t.Fatalf("script error = %v, want exit status 1\n%s", err, out)
			}

			if !strings.Contains(out, tc.wantMsg) {
				t.Errorf("output missing %q:\n%s", tc.wantMsg, out)
			}
		})
	}
}

// TestInitrdNoNetworkScript_FailingUmountKeepsSuccess pins that the cleanup
// trap cannot turn a verified rebuild into a failed seed: a umount that fails
// at exit must not change the script's status.
func TestInitrdNoNetworkScript_FailingUmountKeepsSuccess(t *testing.T) {
	t.Parallel()

	run := defaultInitrdRun(initrdModules("systemd", "base"))
	run.umountRC = 1

	if out, _, err := runInitrdScript(t, run); err != nil {
		t.Fatalf("script failed because umount failed: %v\n%s", err, out)
	}
}

// TestInitrdNoNetworkScript_CleansAptCacheBeforeRebuild pins that the apt
// cache is cleared before dracut runs, since both compete for the template's
// small root filesystem.
func TestInitrdNoNetworkScript_CleansAptCacheBeforeRebuild(t *testing.T) {
	if testing.Short() {
		t.Skip("covered by TestInitrdNoNetworkScript_CleansAptCacheBeforeRebuild_Fast")
	}

	t.Parallel()

	out, calls, err := runInitrdScript(t, defaultInitrdRun(initrdModules("systemd", "base")))
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}

	clean := strings.Index(calls, "apt-get clean")
	dracut := strings.Index(calls, "dracut ")

	if clean < 0 || dracut < 0 || clean > dracut {
		t.Errorf("want apt-get clean before dracut; calls:\n%s", calls)
	}
}

// TestInitrdNoNetworkScript_RequiresHeadroomOnBoot pins the free-space check.
// A rebuild writes a new image beside the old one, so /boot needs the size of
// the largest existing initramfs plus a margin; too little must fail before
// dracut runs, with a message that says how much is needed.
func TestInitrdNoNetworkScript_RequiresHeadroomOnBoot(t *testing.T) {
	if testing.Short() {
		t.Skip("covered by TestInitrdNoNetworkScript_RequiresHeadroomOnBoot_Fast")
	}

	t.Parallel()

	const (
		imageKiB  = 1024
		marginKiB = 65536
	)

	tests := []struct {
		name     string
		availKiB int
		wantFail bool
	}{
		{name: "one KiB short", availKiB: imageKiB + marginKiB - 1, wantFail: true},
		{name: "exactly enough", availKiB: imageKiB + marginKiB},
		{name: "ample", availKiB: 5_000_000},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			run := defaultInitrdRun(initrdModules("systemd", "base"))
			run.availKiB = tc.availKiB

			out, calls, err := runInitrdScript(t, run)

			if !tc.wantFail {
				if err != nil {
					t.Fatalf("script failed with %d KiB free: %v\n%s", tc.availKiB, err, out)
				}

				return
			}

			if err == nil {
				t.Fatalf("script passed with only %d KiB free:\n%s", tc.availKiB, out)
			}

			want := fmt.Sprintf("%d KiB free", tc.availKiB)
			if !strings.Contains(out, want) || !strings.Contains(out, fmt.Sprintf("need %d KiB", imageKiB+marginKiB)) {
				t.Errorf("output should report %q and the %d KiB needed:\n%s", want, imageKiB+marginKiB, out)
			}

			if strings.Contains(calls, "dracut ") {
				t.Errorf("dracut ran despite too little free space; calls:\n%s", calls)
			}
		})
	}
}

// assertScriptOrder fails unless every snippet appears in initrdNoNetworkScript
// and each one comes after the one before it.
func assertScriptOrder(t *testing.T, snippets ...string) {
	t.Helper()

	from := 0

	for _, snippet := range snippets {
		at := strings.Index(initrdNoNetworkScript[from:], snippet)
		if at < 0 {
			t.Fatalf("initrdNoNetworkScript has no %q after offset %d", snippet, from)
		}

		from += at + len(snippet)
	}
}

// TestInitrdNoNetworkScript_VerifiesModuleList_Fast pins the lines that the
// bash run of the module-list test exercises, without starting bash. The
// script reads the dracut module list, fails when systemd is missing from it,
// and fails when any network module is present, each with exit status 1 and
// the message the slow test looks for.
func TestInitrdNoNetworkScript_VerifiesModuleList_Fast(t *testing.T) {
	t.Parallel()

	assertScriptOrder(t,
		"modules=$(lsinitrd -m \"$img\" | sed -n '/^dracut modules:$/,$p')",
		"if ! grep -qx systemd <<<\"$modules\"; then",
		"echo \"ocfp-initrd-no-network: cannot read the module list of $img\" >&2\n    exit 1\n  fi",
		"if grep -xE 'net-lib|systemd-networkd|dyn-netconf|network|network-legacy|network-manager' <<<\"$modules\"; then",
		"echo \"ocfp-initrd-no-network: $img still carries network modules\" >&2\n    exit 1\n  fi",
	)

	// The verification runs once per image, after dracut has rebuilt them.
	assertScriptOrder(t,
		"dracut --quiet --force --regenerate-all --tmpdir \"$stage\"",
		"for img in \"${images[@]}\"; do\n  modules=",
	)
}

// TestInitrdNoNetworkScript_CleansAptCacheBeforeRebuild_Fast pins that the
// apt cache is cleared before the free-space check and before dracut runs,
// without starting bash.
func TestInitrdNoNetworkScript_CleansAptCacheBeforeRebuild_Fast(t *testing.T) {
	t.Parallel()

	assertScriptOrder(t,
		"\napt-get clean\n",
		"avail_kib=$(df -Pk \"$boot\"",
		"dracut --quiet --force --regenerate-all --tmpdir \"$stage\"",
	)
}

// TestInitrdNoNetworkScript_RequiresHeadroomOnBoot_Fast pins the free-space
// check without starting bash. The margin is 65536 KiB, the need is the
// largest image plus the margin, a shortfall fails before dracut with a
// message that reports the free and needed sizes, and the comparison is
// strict so exactly enough passes.
func TestInitrdNoNetworkScript_RequiresHeadroomOnBoot_Fast(t *testing.T) {
	t.Parallel()

	assertScriptOrder(t,
		"margin_kib=65536\n",
		"size_kib=$(( ($(wc -c <\"$img\") + 1023) / 1024 ))",
		"if (( size_kib > largest_kib )); then\n    largest_kib=$size_kib\n  fi",
		"need_kib=$(( largest_kib + margin_kib ))",
		"avail_kib=$(df -Pk \"$boot\" | awk 'NR==2 {print $4}')",
		"if (( avail_kib < need_kib )); then",
		"echo \"ocfp-initrd-no-network: only $avail_kib KiB free under $boot, need $need_kib KiB (largest initramfs $largest_kib KiB plus $margin_kib KiB)\" >&2\n  exit 1\nfi",
		"dracut --quiet --force --regenerate-all --tmpdir \"$stage\"",
	)
}
