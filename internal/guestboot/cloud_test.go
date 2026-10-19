package guestboot

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
)

func TestCloudGuestServices(t *testing.T) {
	for _, provider := range []string{"aws", "gcp"} {
		t.Run(provider, func(t *testing.T) {
			req := &cpi.InstanceRequest{Hostname: "prod-bastion", PublicKey: "ssh-ed25519 AAA", DefaultUsername: "ubuntu", DataDisk: &cpi.DataDiskSpec{Serial: "ocfpdata", Mountpoint: "/data", Filesystem: "ext4", HomeDir: "/home/ubuntu", User: "ubuntu"}, Cloudflare: &cpi.CloudflareSpec{TunnelToken: "secret-token"}}
			data, err := Render(provider, req)
			if err != nil {
				t.Fatal(err)
			}
			if len(data) > 16384 {
				t.Fatalf("userdata exceeds EC2 limit: %d", len(data))
			}
			parts := strings.Split(data, "'")
			if len(parts) < 3 {
				t.Fatal("missing compressed payload")
			}
			raw, err := base64.StdEncoding.DecodeString(parts[1])
			if err != nil {
				t.Fatal(err)
			}
			gz, err := gzip.NewReader(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(gz)
			if err != nil {
				t.Fatal(err)
			}
			script := string(body)
			for _, want := range []string{"/etc/ocfp/guest.json", "ocfp-dataset.service", "ocfp-firstboot.service", "chmod 0600", "systemctl enable", "ocfp-disk-resolve"} {
				if !strings.Contains(script, want) {
					t.Errorf("missing %s", want)
				}
			}
			cmd := exec.Command("bash", "-n")
			cmd.Stdin = strings.NewReader(script)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("shell syntax: %s %v", out, err)
			}
		})
	}
}

func TestCloudDiskResolverNeverGuesses(t *testing.T) {
	for _, want := range []string{"google-ocfp-bastion-data", "skip=384", "findmnt", "lsblk", "multiple"} {
		if !strings.Contains(DiskResolver, want) {
			t.Errorf("missing device safety %s", want)
		}
	}
}

func TestCloudflareWithoutTailscale(t *testing.T) {
	if strings.Contains(CloudFirstboot, "SMBIOS serial empty") {
		t.Fatal("cloud connector must not require a tailscale key")
	}
	if !strings.Contains(CloudFirstboot, "if [[ -n \"$authkey\" ]]") {
		t.Fatal("tailscale setup must be conditional")
	}
}

func TestCloudServiceShellSyntax(t *testing.T) {
	for name, script := range map[string]string{"firstboot": CloudFirstboot, "dataset": cloudDataset(), "watchdog": cloudWatchdog(), "disk": DiskResolver} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command("bash", "-n")
			cmd.Stdin = strings.NewReader(script)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
		})
	}
}

func TestIngressForwardingDoesNotRequireTailscale(t *testing.T) {
	forwarding := strings.Index(CloudFirstboot, "net.ipv4.ip_forward = 1")
	conditional := strings.Index(CloudFirstboot, "if [[ -n \"$authkey\" ]]")
	if forwarding < 0 || conditional < 0 || forwarding > conditional {
		t.Fatal("guest forwarding must be configured before optional Tailscale authentication")
	}
}

func TestCloudDiskResolverAllowsOnlyRecognizedArtifactsZFS(t *testing.T) {
	for _, want := range []string{`"$role" == artifacts`, `"${2:-}" == zfs`, `== zfs_member`, `partitioned data disk refused`} {
		if !strings.Contains(DiskResolver, want) {
			t.Errorf("missing ZFS recovery guard %s", want)
		}
	}
}

func TestCloudCustomUserHasProvisioningPrivileges(t *testing.T) {
	data, err := Render("aws", &cpi.InstanceRequest{DefaultUsername: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(strings.Split(data, "'")[1])
	gz, _ := gzip.NewReader(bytes.NewReader(raw))
	defer gz.Close()
	body, _ := io.ReadAll(gz)
	if !strings.Contains(string(body), "operator ALL=(ALL) NOPASSWD:ALL") {
		t.Fatal("custom SSH user cannot sudo during provision")
	}
}

func TestCloudRecoveryUnits(t *testing.T) {
	data, err := Render("aws", &cpi.InstanceRequest{DataDisk: &cpi.DataDiskSpec{Serial: "ocfpdata", Filesystem: "ext4", Mountpoint: "/data", HomeDir: "/home/ubuntu", User: "ubuntu"}, UserData: "#!/bin/bash\necho ready\n"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(strings.Split(data, "'")[1])
	gz, _ := gzip.NewReader(bytes.NewReader(raw))
	defer gz.Close()
	body, _ := io.ReadAll(gz)
	script := string(body)
	for _, want := range []string{"TimeoutStartSec=660", "Restart=on-failure", "Requires=ocfp-dataset.service", "ocfp-bootstrap.service", "systemctl start --no-block", "ssh.service.d", "tailscaled.service.d", "try-restart --no-block ssh.service tailscaled.service"} {
		if !strings.Contains(script, want) {
			t.Errorf("missing recovery requirement %s", want)
		}
	}
	if strings.Contains(script, "systemctl start ocfp-dataset.service") {
		t.Fatal("cloud-init must not synchronously wait for early boot units")
	}
}
func TestCloudDatasetRefusesForeignMatchingFilesystem(t *testing.T) {
	script := cloudDataset()
	for _, want := range []string{"-s LABEL", "unrecognized data filesystem", "signatures=$(wipefs", "cannot inspect disk signatures", "unexpected mounted data device"} {
		if !strings.Contains(script, want) {
			t.Errorf("missing disk safety %s", want)
		}
	}
}

func TestCloudWatchdogRestartsConnectorWithoutTailscale(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "guest.json")
	log := filepath.Join(dir, "calls")
	if err := os.WriteFile(cfg, []byte(`{"cloudflare":{"token":"test-token"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"systemctl": "#!/bin/bash\necho \"$*\" >> " + log + "\n[[ \"$1\" != is-active ]]\n", "logger": "#!/bin/bash\nexit 0\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	script := strings.ReplaceAll(cloudWatchdog(), "/etc/ocfp/guest.json", cfg)
	script = strings.ReplaceAll(script, "/var/lib/ocfp/firstboot-done", cfg)
	cmd := exec.Command("bash")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("watchdog: %v %s", err, out)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "restart cloudflared") {
		t.Fatalf("connector was not restarted: %s", calls)
	}
}

func TestCloudGuestOSHostnameIndependentOfTailscale(t *testing.T) {
	data, err := Render("aws", &cpi.InstanceRequest{Hostname: "bastion", DomainSuffix: "example.net", Tailscale: &cpi.TailscaleSpec{Hostname: "tailnet-name"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(strings.Split(data, "'")[1])
	gz, _ := gzip.NewReader(bytes.NewReader(raw))
	defer gz.Close()
	body, _ := io.ReadAll(gz)
	for _, want := range []string{`"os_hostname":"bastion.example.net"`, `"hostname":"tailnet-name"`, `hostnamectl set-hostname -- "$os_hostname"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("missing %s", want)
		}
	}
}

func TestCloudDatasetFailsBeforeFormattingUntrustedDisk(t *testing.T) {
	for _, scenario := range []string{"foreign-ext4", "probe-error"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "formatted")
			blkid := "#!/bin/bash\nexit 2\n"
			if scenario == "foreign-ext4" {
				blkid = "#!/bin/bash\ncase \"$*\" in *TYPE*) echo ext4;; *LABEL*) echo foreign;; esac\n"
			}
			for name, body := range map[string]string{"blkid": blkid, "wipefs": "#!/bin/bash\nexit 1\n", "mkfs.ext4": "#!/bin/bash\ntouch " + marker + "\n"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			script := cloudDataset()
			start := strings.Index(script, "EXISTING=")
			end := strings.Index(script, `mkdir -p "${MOUNT}"`)
			cmd := exec.Command("bash")
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
			cmd.Stdin = strings.NewReader("log() { echo \"$*\"; }\nREAL=/dev/mock\nFSTYPE=ext4\n" + script[start:end])
			if out, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("unsafe disk accepted: %s", out)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("untrusted disk formatted")
			}
		})
	}
}

func TestCloudDatasetDoesNotRequirePVESerial(t *testing.T) {
	if strings.Contains(cloudDataset(), "data block carries no disk serial") {
		t.Fatal("native cloud attachment resolution must not return success because PVE serial is empty")
	}
}

func TestCloudTailscaleHonorsExplicitSSHFalse(t *testing.T) {
	for _, script := range []string{CloudFirstboot, cloudWatchdog()} {
		if strings.Contains(script, ".ssh // true") {
			t.Fatal("jq // treats explicit false as absent and enables Tailscale SSH")
		}
	}
}
