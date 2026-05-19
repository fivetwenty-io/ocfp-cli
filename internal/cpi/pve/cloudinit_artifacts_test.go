package pve

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
		"- awscli",
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

	// Error message must name the missing fields so callers can diagnose quickly.
	for _, field := range []string{"AccessKey", "SecretKey", "DownloadURL", "S3Port", "ConsolePort", "Mountpoint", "ZFSDataset"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("error %q does not mention missing field %q", err.Error(), field)
		}
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
