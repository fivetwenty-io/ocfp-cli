package providers

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/pem"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sshkey "golang.org/x/crypto/ssh"

	"github.com/fivetwenty-io/ocfp-cli/internal/config"
	"github.com/fivetwenty-io/ocfp-cli/internal/state"
)

const dryRunBlocName = "dryrun-bloc"

// dryRunHome points every directory the providers read or write at temp
// dirs, clears the environment variables that name a bastion IP or key,
// and sends any HTTP request to a closed local port, so no test reaches
// the real home directory or the network. It returns the roots to watch.
func dryRunHome(t *testing.T) []string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OCFP_HOME", "")

	roots := []string{home}

	for _, key := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME"} {
		dir := t.TempDir()
		t.Setenv(key, dir)
		roots = append(roots, dir)
	}

	for _, key := range []string{
		"AWS_BASTION_IP", "GCP_BASTION_IP", "OCFP_BASTION_IP", "PVE_BASTION_IP", "STACKIT_BASTION_IP",
		"OCFP_SSH_KEY", "OCFP_BASTION_USER",
	} {
		t.Setenv(key, "")
	}

	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		t.Setenv(key, "http://127.0.0.1:9")
	}

	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	// The PVE and STACKIT lookups read terraform state relative to the
	// working directory.
	t.Chdir(t.TempDir())

	return roots
}

// fileTree records every path under roots, with each file's mode and a
// digest of its content.
func fileTree(t *testing.T, roots ...string) map[string]string {
	t.Helper()

	tree := map[string]string{}

	for _, root := range roots {
		require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			info, err := entry.Info()
			if err != nil {
				return err
			}

			if !info.Mode().IsRegular() {
				tree[path] = info.Mode().String()

				return nil
			}

			data, err := os.ReadFile(path) // #nosec G304 -- path is under a test temp dir
			if err != nil {
				return err
			}

			tree[path] = fmt.Sprintf("%s %x", info.Mode(), sha256.Sum256(data))

			return nil
		}))
	}

	return tree
}

// testKeyPEM returns a new ed25519 private key in OpenSSH form, protected
// by passphrase when it isn't empty.
func testKeyPEM(t *testing.T, passphrase string) string {
	t.Helper()

	_, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	var block *pem.Block
	if passphrase == "" {
		block, err = sshkey.MarshalPrivateKey(key, "")
	} else {
		block, err = sshkey.MarshalPrivateKeyWithPassphrase(key, "", []byte(passphrase))
	}

	require.NoError(t, err)

	return string(pem.EncodeToMemory(block))
}

// fakeSSHPass puts an sshpass first on PATH, so the providers that check
// for it find it on every machine.
func fakeSSHPass(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "sshpass"), []byte("#!/bin/sh\nexit 0\n"), 0o700)) //nolint:gosec // an executable fake
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// connectionDetailer is the pair of lookups each provider offers.
type connectionDetailer interface {
	GetConnectionDetails(ctx context.Context) (*ConnectionDetails, error)
	PreviewConnectionDetails(ctx context.Context) (*ConnectionDetails, error)
}

// providerCase is one provider whose connection lookup writes on a real
// run, set up so that the lookup opens the bloc's missing state directory
// and restores the bloc's key from the config.
type providerCase struct {
	name    string
	keyName string
	host    string
	// build returns the provider for cfg, after setting whatever gives it
	// the bastion's IP once the state lookup comes up empty.
	build func(t *testing.T, cfg *config.Config) connectionDetailer
	// sshpass is whether the provider answers an encrypted key with the
	// bloc's name as its passphrase, passed through sshpass. GCP doesn't.
	sshpass bool
}

func ec2ReturningIP(ip string) *fakeEC2Client {
	return &fakeEC2Client{output: &ec2.DescribeInstancesOutput{Reservations: []types.Reservation{{
		Instances: []types.Instance{{InstanceId: aws.String("i-0dryrun"), PublicIpAddress: aws.String(ip)}},
	}}}}
}

var providerCases = []providerCase{
	{"aws", dryRunBlocName + "-bastion", "54.0.0.10", func(_ *testing.T, cfg *config.Config) connectionDetailer {
		a := NewAWSBastionInit(cfg)
		a.ec2Client = ec2ReturningIP("54.0.0.10")

		return a
	}, true},
	{"pve", dryRunBlocName + "-keypair", "10.0.0.20", func(t *testing.T, cfg *config.Config) connectionDetailer {
		t.Helper()
		t.Setenv("PVE_BASTION_IP", "10.0.0.20")

		return NewPVEBastionInit(cfg)
	}, true},
	{"stackit", dryRunBlocName + "-keypair", "10.0.0.30", func(t *testing.T, cfg *config.Config) connectionDetailer {
		t.Helper()
		t.Setenv("STACKIT_BASTION_IP", "10.0.0.30")

		return NewStackitBastionInit(cfg)
	}, true},
	{"gcp", dryRunBlocName + "-keypair", "10.0.0.40", func(t *testing.T, cfg *config.Config) connectionDetailer {
		t.Helper()
		t.Setenv("OCFP_BASTION_IP", "10.0.0.40")

		return NewGCPBastionInit(cfg)
	}, false},
}

// A dry run's connection lookup writes nothing. Where the real lookup
// creates the bloc's state directory and restores the bloc's SSH key from
// the config, the dry run reads only, and it returns the details the real
// lookup returns, with the path the restored key would have.
func TestPreviewConnectionDetailsWritesNothing(t *testing.T) {
	for _, tc := range providerCases {
		for _, passphrase := range []string{"", dryRunBlocName} {
			// The name becomes part of the key's path, so it keeps to the
			// characters that a key path may hold.
			t.Run(tc.name+map[bool]string{true: "_encrypted", false: "_plain"}[passphrase != ""], func(t *testing.T) {
				roots := dryRunHome(t)
				fakeSSHPass(t)
				cfg := &config.Config{Name: dryRunBlocName, Keys: map[string]string{tc.keyName: testKeyPEM(t, passphrase)}}
				provider := tc.build(t, cfg)
				keyPath := filepath.Join(config.OcfpSSHKeyDir(dryRunBlocName), "id_ed25519")

				before := fileTree(t, roots...)
				preview, err := provider.PreviewConnectionDetails(context.Background())
				require.NoError(t, err)
				assert.Equal(t, before, fileTree(t, roots...), "a dry run must write nothing")
				assert.Equal(t, keyPath, preview.PrivateKeyPath)
				assert.Equal(t, tc.host, preview.Host)

				encrypted := passphrase != "" && tc.sshpass
				assert.Equal(t, encrypted, preview.UseSSHPass, "an encrypted key goes through sshpass")
				assert.Equal(t, map[bool]string{true: dryRunBlocName, false: ""}[encrypted], preview.Password,
					"an encrypted key's passphrase is the bloc's name")

				real, err := provider.GetConnectionDetails(context.Background())
				require.NoError(t, err)
				assert.Equal(t, real, preview, "a dry run must find what the real run finds")

				_, err = os.Stat(keyPath)
				require.NoError(t, err, "the real run restores the key, so the dry run is held to the same path")
			})
		}
	}
}

// The real lookups that consult the bloc's state create its state
// directory, so the test above holds the dry runs of those lookups to
// writing nothing there.
func TestGetConnectionDetailsCreatesTheStateDirectory(t *testing.T) {
	for _, tc := range providerCases {
		if tc.name == "gcp" {
			continue // the environment's IP comes before the state for GCP
		}

		t.Run(tc.name, func(t *testing.T) {
			dryRunHome(t)
			cfg := &config.Config{Name: dryRunBlocName, Keys: map[string]string{tc.keyName: testKeyPEM(t, "")}}
			provider := tc.build(t, cfg)

			stateDir, err := state.GetStateDir(dryRunBlocName)
			require.NoError(t, err)

			_, err = provider.GetConnectionDetails(context.Background())
			require.NoError(t, err)

			_, err = os.Stat(stateDir)
			require.NoError(t, err)
		})
	}
}

// GCP reads the state only when nothing else names the bastion's IP, and
// then fails when the state doesn't name it either. The dry run fails with
// the same error and creates no state directory.
func TestGCPPreviewFailsLikeTheRealLookupWithoutWriting(t *testing.T) {
	roots := dryRunHome(t)
	provider := NewGCPBastionInit(&config.Config{Name: dryRunBlocName})

	stateDir, err := state.GetStateDir(dryRunBlocName)
	require.NoError(t, err)

	before := fileTree(t, roots...)
	_, previewErr := provider.PreviewConnectionDetails(context.Background())
	require.Error(t, previewErr)
	assert.Equal(t, before, fileTree(t, roots...), "a dry run must write nothing")

	_, realErr := provider.GetConnectionDetails(context.Background())
	require.Error(t, realErr)
	assert.Equal(t, realErr.Error(), previewErr.Error())

	_, err = os.Stat(stateDir)
	require.NoError(t, err, "the real lookup creates the state directory")
}

// A dry run refuses a bloc name that the key restore would refuse, with
// the same error, and writes nothing.
func TestPreviewConnectionDetailsRefusesABadBlocNameLikeTheRestore(t *testing.T) {
	for _, tc := range providerCases {
		t.Run(tc.name, func(t *testing.T) {
			roots := dryRunHome(t)
			bad := "dry/run"
			keyName := bad + tc.keyName[len(dryRunBlocName):]
			cfg := &config.Config{Name: bad, Keys: map[string]string{keyName: testKeyPEM(t, "")}}
			provider := tc.build(t, cfg)

			before := fileTree(t, roots...)
			_, previewErr := provider.PreviewConnectionDetails(context.Background())
			assert.Equal(t, before, fileTree(t, roots...), "a dry run must write nothing")

			_, realErr := provider.GetConnectionDetails(context.Background())
			require.Error(t, realErr)
			require.Error(t, previewErr)
			assert.Equal(t, realErr.Error(), previewErr.Error())
		})
	}
}

// The providers that aren't implemented fail the dry run as they fail the
// real lookup.
func TestPreviewConnectionDetailsOfUnimplementedProviders(t *testing.T) {
	cfg := &config.Config{Name: dryRunBlocName}

	for name, provider := range map[string]connectionDetailer{
		"azure":     NewAzureBastionInit(cfg),
		"openstack": NewOpenStackBastionInit(cfg),
		"vmware":    NewVMwareBastionInit(cfg),
	} {
		t.Run(name, func(t *testing.T) {
			_, realErr := provider.GetConnectionDetails(context.Background())
			_, previewErr := provider.PreviewConnectionDetails(context.Background())
			require.Error(t, previewErr)
			assert.Equal(t, realErr, previewErr)
		})
	}
}

// legacyEncryptedPEM is a PEM key whose header says it is encrypted. The
// restore writes it as it stands, and the encryption check reads only the
// header.
const legacyEncryptedPEM = "-----BEGIN RSA PRIVATE KEY-----\n" +
	"Proc-Type: 4,ENCRYPTED\n" +
	"DEK-Info: AES-128-CBC,00112233445566778899AABBCCDDEEFF\n\n" +
	"bm90IGEga2V5IHVudGlsIGl0IGlzIGRlY3J5cHRlZA==\n" +
	"-----END RSA PRIVATE KEY-----\n"

// The real lookup refuses to read a restored key whose path holds a
// character a key path may not, such as a space in the data directory, and
// treats the key as unencrypted. The dry run reads the config's copy
// instead, so it must refuse the path the same way to find what the real
// lookup finds.
func TestPreviewConnectionDetailsRefusesTheKeyPathTheRealLookupRefuses(t *testing.T) {
	for _, tc := range providerCases {
		t.Run(tc.name, func(t *testing.T) {
			dryRunHome(t)

			dataHome := filepath.Join(t.TempDir(), "with space")
			require.NoError(t, os.MkdirAll(dataHome, 0o700))
			t.Setenv("XDG_DATA_HOME", dataHome)

			cfg := &config.Config{Name: dryRunBlocName, Keys: map[string]string{tc.keyName: legacyEncryptedPEM}}
			provider := tc.build(t, cfg)

			preview, err := provider.PreviewConnectionDetails(context.Background())
			require.NoError(t, err)

			real, err := provider.GetConnectionDetails(context.Background())
			require.NoError(t, err)
			assert.Equal(t, real, preview, "a dry run must find what the real run finds")
			assert.Empty(t, preview.Password, "a key the lookup can't read counts as unencrypted")
		})
	}
}
