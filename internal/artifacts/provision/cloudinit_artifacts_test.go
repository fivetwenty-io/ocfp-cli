package provision

import (
	"strings"
	"testing"
)

// fixedArtifactsInputs returns a fully-populated ArtifactsCloudInitInputs
// suitable for golden-file assertions. All values are chosen to be
// unambiguous substrings that cannot collide with surrounding template text.
func fixedArtifactsInputs(tlsEnabled bool) ArtifactsCloudInitInputs {
	in := ArtifactsCloudInitInputs{
		AccessKey:   "AKIATEST1234567890AB",
		SecretKey:   "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY12",
		DownloadURL: "https://dl.rustfs.com/artifacts/1.0.0-beta.3/rustfs-linux-amd64.zip",
		S3Port:      9000,
		ConsolePort: 9001,
		Mountpoint:  "/data",
		Filesystem:  "zfs",
		ZFSDataset:  "rpool/myblock",
		TLSEnabled:  tlsEnabled,
	}

	if tlsEnabled {
		in.CertPEM = "-----BEGIN CERTIFICATE-----\nMIIBpTCCAUugAwIBAgIUtest\n-----END CERTIFICATE-----"
		in.KeyPEM = "-----BEGIN EC PRIVATE KEY-----\nMHQCAQEEItest\n-----END EC PRIVATE KEY-----"
	}

	return in
}

// TestRenderArtifactsCloudInit_NoTLS checks that the rendered output contains
// all required structural and credential substrings when TLS is disabled.
func TestRenderArtifactsCloudInit_NoTLS(t *testing.T) {
	t.Parallel()

	out, err := RenderArtifactsCloudInit(fixedArtifactsInputs(false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	checks := []string{
		"#cloud-config",
		"package_update: true",
		"- zfsutils-linux",
		"- unzip",
		"- jq",
		// env file
		"path: /etc/default/rustfs",
		"permissions: '0640'",
		"owner: rustfs:rustfs",
		"RUSTFS_ADDRESS=:9000",
		"RUSTFS_CONSOLE_ADDRESS=:9001",
		"RUSTFS_ACCESS_KEY=AKIATEST1234567890AB",
		"RUSTFS_SECRET_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY12",
		"RUSTFS_VOLUMES=/data",
		// systemd unit
		"path: /etc/systemd/system/rustfs.service",
		"Description=RustFS S3 server",
		"After=network-online.target zfs-mount.service",
		"EnvironmentFile=/etc/default/rustfs",
		"ExecStart=/usr/local/bin/rustfs",
		// runcmd
		"runcmd:",
		"useradd --system --home /var/lib/rustfs --shell /usr/sbin/nologin rustfs",
		"zpool import -a || true",
		"zpool create -f -o ashift=12 rpool /dev/sdb",
		"zfs create -o mountpoint=/data -o compression=lz4 rpool/myblock",
		"chown -R rustfs:rustfs /data",
		"mkdir -p /opt/rustfs/tls /var/log/rustfs",
		"curl -fsSL https://dl.rustfs.com/artifacts/1.0.0-beta.3/rustfs-linux-amd64.zip -o /tmp/rustfs.zip",
		"unzip -d /usr/local/bin /tmp/rustfs.zip",
		"chmod +x /usr/local/bin/rustfs",
		"systemctl daemon-reload",
		"systemctl enable --now rustfs",
	}

	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n----\n%s", want, out)
		}
	}
}

// TestRenderArtifactsCloudInit_NoTLS_TLSBlocksAbsent ensures TLS-specific
// write_files entries are absent when TLSEnabled=false.
func TestRenderArtifactsCloudInit_NoTLS_TLSBlocksAbsent(t *testing.T) {
	t.Parallel()

	out, err := RenderArtifactsCloudInit(fixedArtifactsInputs(false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	absent := []string{
		"rustfs_cert.pem",
		"rustfs_key.pem",
		"RUSTFS_TLS_PATH",
		"BEGIN CERTIFICATE",
		"BEGIN EC PRIVATE KEY",
	}

	for _, unwanted := range absent {
		if strings.Contains(out, unwanted) {
			t.Errorf("output should not contain %q when TLS disabled\n----\n%s", unwanted, out)
		}
	}
}

// TestRenderArtifactsCloudInit_TLSEnabled checks that cert and key PEM blocks
// appear with correct 6-space indentation when TLSEnabled=true.
func TestRenderArtifactsCloudInit_TLSEnabled(t *testing.T) {
	t.Parallel()

	out, err := RenderArtifactsCloudInit(fixedArtifactsInputs(true))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	checks := []string{
		"path: /opt/rustfs/tls/rustfs_cert.pem",
		"permissions: '0640'",
		"path: /opt/rustfs/tls/rustfs_key.pem",
		"permissions: '0600'",
		"RUSTFS_TLS_PATH=/opt/rustfs/tls",
		// PEM lines must be indented by 6 spaces inside the content block
		"      -----BEGIN CERTIFICATE-----",
		"      MIIBpTCCAUugAwIBAgIUtest",
		"      -----END CERTIFICATE-----",
		"      -----BEGIN EC PRIVATE KEY-----",
		"      MHQCAQEEItest",
		"      -----END EC PRIVATE KEY-----",
	}

	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n----\n%s", want, out)
		}
	}
}

// TestRenderArtifactsCloudInit_TLSEnabled_CoreFieldsPresent ensures all
// non-TLS required fields also appear correctly in TLS-enabled output.
func TestRenderArtifactsCloudInit_TLSEnabled_CoreFieldsPresent(t *testing.T) {
	t.Parallel()

	out, err := RenderArtifactsCloudInit(fixedArtifactsInputs(true))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	checks := []string{
		"RUSTFS_ACCESS_KEY=AKIATEST1234567890AB",
		"RUSTFS_SECRET_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY12",
		"zfs create -o mountpoint=/data -o compression=lz4 rpool/myblock",
		"https://dl.rustfs.com/artifacts/1.0.0-beta.3/rustfs-linux-amd64.zip",
	}

	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n----\n%s", want, out)
		}
	}
}

// TestRenderArtifactsCloudInit_EmptyInputsError asserts that zero-value inputs
// return an error and no output.
func TestRenderArtifactsCloudInit_EmptyInputsError(t *testing.T) {
	t.Parallel()

	out, err := RenderArtifactsCloudInit(ArtifactsCloudInitInputs{})
	if err == nil {
		t.Fatalf("expected error for empty inputs, got nil (output len %d)", len(out))
	}

	if out != "" {
		t.Errorf("expected empty output on error, got %d bytes", len(out))
	}

	// Error message must name the missing fields so callers can diagnose
	// quickly. ZFSDataset is absent: the filesystem defaults to ext4, which
	// does not need a dataset.
	for _, field := range []string{"AccessKey", "SecretKey", "DownloadURL", "S3Port", "ConsolePort", "Mountpoint"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("error %q does not mention missing field %q", err.Error(), field)
		}
	}

	if strings.Contains(err.Error(), "ZFSDataset") {
		t.Errorf("error %q should not require ZFSDataset for the default (ext4) filesystem", err.Error())
	}
}

// TestRenderArtifactsCloudInit_DefaultExt4 verifies that leaving Filesystem
// empty renders the ext4 path: mkfs + fstab mount, no ZFS tooling, and no
// zfs-mount.service ordering.
func TestRenderArtifactsCloudInit_DefaultExt4(t *testing.T) {
	t.Parallel()

	in := fixedArtifactsInputs(false)
	in.Filesystem = ""
	in.ZFSDataset = ""

	out, err := RenderArtifactsCloudInit(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, want := range []string{
		"mkfs.ext4 /dev/sdb",
		"blkid -o value -s UUID /dev/sdb",
		"/data ext4 defaults,nofail 0 2",
		"After=network-online.target\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ext4 output missing %q\n----\n%s", want, out)
		}
	}

	for _, unwanted := range []string{"zfsutils-linux", "zpool", "zfs create", "zfs-mount.service", "xfsprogs"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("ext4 output should not contain %q\n----\n%s", unwanted, out)
		}
	}
}

// TestRenderArtifactsCloudInit_XFS verifies the xfs branch installs xfsprogs
// and formats with mkfs.xfs.
func TestRenderArtifactsCloudInit_XFS(t *testing.T) {
	t.Parallel()

	in := fixedArtifactsInputs(false)
	in.Filesystem = "xfs"
	in.ZFSDataset = ""

	out, err := RenderArtifactsCloudInit(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, want := range []string{"- xfsprogs", "mkfs.xfs /dev/sdb", "/data xfs defaults,nofail 0 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("xfs output missing %q\n----\n%s", want, out)
		}
	}
}

// TestRenderArtifactsCloudInit_ZFSRequiresDataset asserts that Filesystem=zfs
// without a dataset fails validation.
func TestRenderArtifactsCloudInit_ZFSRequiresDataset(t *testing.T) {
	t.Parallel()

	in := fixedArtifactsInputs(false)
	in.ZFSDataset = ""

	_, err := RenderArtifactsCloudInit(in)
	if err == nil || !strings.Contains(err.Error(), "ZFSDataset") {
		t.Errorf("expected ZFSDataset error for zfs without dataset, got %v", err)
	}
}

// TestRenderArtifactsCloudInit_InvalidFilesystemError asserts unsupported
// filesystem values are rejected.
func TestRenderArtifactsCloudInit_InvalidFilesystemError(t *testing.T) {
	t.Parallel()

	in := fixedArtifactsInputs(false)
	in.Filesystem = "btrfs"

	_, err := RenderArtifactsCloudInit(in)
	if err == nil || !strings.Contains(err.Error(), "btrfs") {
		t.Errorf("expected unsupported-filesystem error, got %v", err)
	}
}

// TestRenderArtifactsCloudInit_TLSEnabled_MissingCertError asserts that
// TLSEnabled=true without PEM content returns an error.
func TestRenderArtifactsCloudInit_TLSEnabled_MissingCertError(t *testing.T) {
	t.Parallel()

	in := fixedArtifactsInputs(true)
	in.CertPEM = ""

	_, err := RenderArtifactsCloudInit(in)
	if err == nil {
		t.Fatal("expected error when CertPEM empty and TLSEnabled=true")
	}

	if !strings.Contains(err.Error(), "CertPEM") {
		t.Errorf("error %q does not mention CertPEM", err.Error())
	}
}

// TestRenderArtifactsCloudInit_TLSEnabled_MissingKeyError asserts that
// TLSEnabled=true without key PEM content returns an error.
func TestRenderArtifactsCloudInit_TLSEnabled_MissingKeyError(t *testing.T) {
	t.Parallel()

	in := fixedArtifactsInputs(true)
	in.KeyPEM = ""

	_, err := RenderArtifactsCloudInit(in)
	if err == nil {
		t.Fatal("expected error when KeyPEM empty and TLSEnabled=true")
	}

	if !strings.Contains(err.Error(), "KeyPEM") {
		t.Errorf("error %q does not mention KeyPEM", err.Error())
	}
}

// TestRenderArtifactsCloudInit_CAPEM_InstallsVMTrustStore asserts that
// setting CAPEM (task 4.4, VM self-trust) writes the trust anchor into the
// VM's OS trust store location and refreshes it via update-ca-certificates,
// and that the ca-certificates package is requested.
func TestRenderArtifactsCloudInit_CAPEM_InstallsVMTrustStore(t *testing.T) {
	t.Parallel()

	in := fixedArtifactsInputs(true)
	in.CAPEM = "-----BEGIN CERTIFICATE-----\nMIICAtest\n-----END CERTIFICATE-----"

	out, err := RenderArtifactsCloudInit(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, want := range []string{
		"- ca-certificates",
		"path: /usr/local/share/ca-certificates/ocfp-ca.crt",
		"      -----BEGIN CERTIFICATE-----",
		"      MIICAtest",
		"update-ca-certificates",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n----\n%s", want, out)
		}
	}
}

// TestRenderArtifactsCloudInit_NoCAPEM_OmitsVMTrustStore asserts CAPEM is a
// clean no-op when empty: no trust-store file, no update-ca-certificates
// call, no extra ca-certificates package — matches disabled/self-signed
// callers that never resolved a CA (or providers not yet on task 4.4).
func TestRenderArtifactsCloudInit_NoCAPEM_OmitsVMTrustStore(t *testing.T) {
	t.Parallel()

	out, err := RenderArtifactsCloudInit(fixedArtifactsInputs(true))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, unwanted := range []string{
		"ocfp-ca.crt",
		"update-ca-certificates",
		"- ca-certificates",
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("output should not contain %q when CAPEM is empty\n----\n%s", unwanted, out)
		}
	}
}

// TestIndentPEM verifies that each line in the PEM block is prefixed with
// exactly n spaces and the output terminates with a newline.
func TestIndentPEM(t *testing.T) {
	t.Parallel()

	pem := "-----BEGIN CERTIFICATE-----\nMIIdata\n-----END CERTIFICATE-----"
	got := indentPEM(6, pem)

	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), got)
	}

	for _, line := range lines {
		if !strings.HasPrefix(line, "      ") {
			t.Errorf("line %q not indented by 6 spaces", line)
		}
	}

	if !strings.HasSuffix(got, "\n") {
		t.Error("indentPEM output must end with newline")
	}
}
