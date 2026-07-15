package pve

import (
	"strings"
	"testing"
)

// validArtifactsInputs returns a fully-populated input set (TLS on) for the
// provision-script tests. Individual tests mutate copies as needed.
func validArtifactsInputs() ArtifactsCloudInitInputs {
	return ArtifactsCloudInitInputs{
		AccessKey:   "AKIAEXAMPLE",
		SecretKey:   "s3cr3t-key-value",
		DownloadURL: "https://dl.example.com/rustfs-1.2.3-linux-amd64.zip",
		S3Port:      9000,
		ConsolePort: 9001,
		Mountpoint:  "/data/rustfs",
		Filesystem:  "zfs",
		ZFSDataset:  "rpool/rustfs",
		TLSEnabled:  true,
		CertPEM:     "-----BEGIN CERTIFICATE-----\nMIIBcert\n-----END CERTIFICATE-----",
		KeyPEM:      "-----BEGIN PRIVATE KEY-----\nMIIBkey\n-----END PRIVATE KEY-----",
	}
}

func TestRenderArtifactsProvisionScript_ShebangAndStrictMode(t *testing.T) {
	t.Parallel()

	got, err := RenderArtifactsProvisionScript(validArtifactsInputs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.HasPrefix(got, "#!/usr/bin/env bash\n") {
		t.Errorf("script must start with bash shebang, got first line: %q", firstLine(got))
	}

	if !strings.Contains(got, "set -euo pipefail") {
		t.Error("script must use strict mode (set -euo pipefail)")
	}
}

func TestRenderArtifactsProvisionScript_InstallsPackages(t *testing.T) {
	t.Parallel()

	got, err := RenderArtifactsProvisionScript(validArtifactsInputs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, pkg := range []string{"zfsutils-linux", "unzip", "jq"} {
		if !strings.Contains(got, pkg) {
			t.Errorf("script must install package %q", pkg)
		}
	}

	if !strings.Contains(got, "apt-get install") {
		t.Error("script must apt-get install dependencies")
	}

	// awscli has no apt install candidate on Ubuntu Noble (24.04); requiring it
	// aborts the strict-mode script. Bucket operations run operator-side via the
	// Go S3 SDK, so the VM never needs it.
	if strings.Contains(got, "awscli") {
		t.Error("script must not apt-install awscli (no candidate on Noble; not needed on the VM)")
	}
}

func TestRenderArtifactsProvisionScript_WritesEnvAndService(t *testing.T) {
	t.Parallel()

	in := validArtifactsInputs()

	got, err := RenderArtifactsProvisionScript(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantEnv := []string{
		"/etc/default/rustfs",
		"RUSTFS_ADDRESS=:9000",
		"RUSTFS_CONSOLE_ADDRESS=:9001",
		"RUSTFS_ACCESS_KEY=AKIAEXAMPLE",
		"RUSTFS_SECRET_KEY=s3cr3t-key-value",
		"RUSTFS_VOLUMES=/data/rustfs",
	}
	for _, w := range wantEnv {
		if !strings.Contains(got, w) {
			t.Errorf("env config missing %q", w)
		}
	}

	if !strings.Contains(got, "/etc/systemd/system/rustfs.service") {
		t.Error("script must write the rustfs systemd unit")
	}
}

func TestRenderArtifactsProvisionScript_TLSMaterialAndPath(t *testing.T) {
	t.Parallel()

	got, err := RenderArtifactsProvisionScript(validArtifactsInputs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, w := range []string{
		"RUSTFS_TLS_PATH=/opt/rustfs/tls",
		"/opt/rustfs/tls/rustfs_cert.pem",
		"/opt/rustfs/tls/rustfs_key.pem",
		"-----BEGIN CERTIFICATE-----",
		"-----BEGIN PRIVATE KEY-----",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("TLS-enabled script missing %q", w)
		}
	}
}

func TestRenderArtifactsProvisionScript_TLSDisabledOmitsMaterial(t *testing.T) {
	t.Parallel()

	in := validArtifactsInputs()
	in.TLSEnabled = false
	in.CertPEM = ""
	in.KeyPEM = ""

	got, err := RenderArtifactsProvisionScript(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(got, "RUSTFS_TLS_PATH") {
		t.Error("TLS-disabled script must not set RUSTFS_TLS_PATH")
	}

	if strings.Contains(got, "rustfs_cert.pem") {
		t.Error("TLS-disabled script must not write cert material")
	}
}

func TestRenderArtifactsProvisionScript_IdempotentZpoolAndBinary(t *testing.T) {
	t.Parallel()

	got, err := RenderArtifactsProvisionScript(validArtifactsInputs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// zpool create must be guarded so re-runs don't fail on an existing pool.
	if !strings.Contains(got, "zpool list rpool") {
		t.Error("zpool creation must be guarded by an existence check")
	}

	if !strings.Contains(got, "zpool create") || !strings.Contains(got, "/dev/sdb") {
		t.Error("script must create the rpool zpool on /dev/sdb")
	}

	// dataset creation guarded too.
	if !strings.Contains(got, "rpool/rustfs") {
		t.Error("script must create the configured ZFS dataset")
	}

	for _, w := range []string{
		"https://dl.example.com/rustfs-1.2.3-linux-amd64.zip",
		"unzip",
		"/usr/local/bin/rustfs",
		"systemctl daemon-reload",
		"systemctl enable --now rustfs",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("script missing install/enable step %q", w)
		}
	}
}

func TestRenderArtifactsProvisionScript_DefaultExt4(t *testing.T) {
	t.Parallel()

	in := validArtifactsInputs()
	in.Filesystem = ""
	in.ZFSDataset = ""

	got, err := RenderArtifactsProvisionScript(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, w := range []string{
		"mkfs.ext4 /dev/sdb",
		"/data/rustfs ext4 defaults,nofail 0 2",
		"After=network-online.target\n",
	} {
		if !strings.Contains(got, w) {
			t.Errorf("ext4 script missing %q", w)
		}
	}

	for _, unwanted := range []string{"zfsutils-linux", "zpool", "zfs create", "zfs-mount.service"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("ext4 script should not contain %q", unwanted)
		}
	}
}

func TestRenderArtifactsProvisionScript_XFSInstallsXfsprogs(t *testing.T) {
	t.Parallel()

	in := validArtifactsInputs()
	in.Filesystem = "xfs"
	in.ZFSDataset = ""

	got, err := RenderArtifactsProvisionScript(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, w := range []string{"xfsprogs", "mkfs.xfs /dev/sdb"} {
		if !strings.Contains(got, w) {
			t.Errorf("xfs script missing %q", w)
		}
	}
}

func TestRenderArtifactsProvisionScript_ValidatesInputs(t *testing.T) {
	t.Parallel()

	in := validArtifactsInputs()
	in.AccessKey = ""

	if _, err := RenderArtifactsProvisionScript(in); err == nil {
		t.Error("expected validation error when AccessKey is empty")
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")

	return line
}
