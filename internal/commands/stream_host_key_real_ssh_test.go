package commands

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	bastionssh "github.com/fivetwenty-io/ocfp-cli/internal/bastion/ssh"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// localSSHServer serves ssh on 127.0.0.1 to any client. Each command it is
// asked to run writes stderr to the channel's stderr and exits with status.
// It returns the server's port.
func localSSHServer(t *testing.T, stderr string, status uint32) int {
	t.Helper()

	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	signer, err := gossh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}

	config := &gossh.ServerConfig{NoClientAuth: true} //nolint:exhaustruct // a test server needs no more
	config.AddHostKey(signer)

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0") //nolint:exhaustruct // defaults
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			go serveLocalSSH(conn, config, stderr, status)
		}
	}()

	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address %v is not TCP", listener.Addr())
	}

	return addr.Port
}

func serveLocalSSH(conn net.Conn, config *gossh.ServerConfig, stderr string, status uint32) {
	_, channels, requests, err := gossh.NewServerConn(conn, config)
	if err != nil {
		_ = conn.Close()

		return
	}

	go gossh.DiscardRequests(requests)

	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(gossh.UnknownChannelType, "sessions only")

			continue
		}

		channel, channelRequests, acceptErr := newChannel.Accept()
		if acceptErr != nil {
			continue
		}

		go func() {
			for request := range channelRequests {
				if request.Type != "exec" {
					_ = request.Reply(request.Type == "env", nil)

					continue
				}

				_ = request.Reply(true, nil)
				_, _ = fmt.Fprint(channel.Stderr(), stderr)

				exitStatus := make([]byte, 4) //nolint:mnd // a uint32
				binary.BigEndian.PutUint32(exitStatus, status)
				_, _ = channel.SendRequest("exit-status", false, exitStatus)
				_ = channel.Close()

				return
			}
		}()
	}
}

// localSSHCommand is the ssh command executeSSH would run against the local
// server, checking its key as the bastion's is checked and reading nothing
// from the real home directory.
func localSSHCommand(t *testing.T, port int) []string {
	t.Helper()

	hostKey, err := bastionssh.BastionHostKeyOptions()
	if err != nil {
		t.Fatal(err)
	}

	cmd := []string{"ssh", "-F", "/dev/null"}
	cmd = append(cmd, hostKey...)

	return append(cmd,
		"-o", "LogLevel=ERROR",
		"-o", "BatchMode=yes",
		"-o", "IdentityAgent=none",
		"-o", "IdentityFile="+filepath.Join(t.TempDir(), "none"),
		"-p", strconv.Itoa(port),
		"u@127.0.0.1", "true")
}

func realSSHHome(t *testing.T) string {
	t.Helper()

	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh is not installed")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)

	err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700)
	if err != nil {
		t.Fatal(err)
	}

	return filepath.Join(home, ".ssh", "known_hosts")
}

// A command the bastion runs can print ssh's refusal, even naming a file
// at our own known_hosts path, and exit 255. That is the command's failure,
// not a refusal of the bastion's key.
func TestRealSSHRemoteRefusalIsTheCommandsFailure(t *testing.T) {
	knownHosts := realSSHHome(t)

	remote := "@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\n" +
		"Offending ECDSA key in " + knownHosts + ":3\n" +
		"Host key for 10.64.64.20 has changed and you have requested strict checking.\n" +
		"Host key verification failed.\n"
	port := localSSHServer(t, remote, 255) //nolint:mnd // the status a failed ssh exits with

	err := executeSSH(context.Background(), localSSHCommand(t, port), "127.0.0.1")
	if errors.Is(err, bastionssh.ErrHostKeyMismatch) {
		t.Fatalf("got %v, want the remote command's failure", err)
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 255 {
		t.Fatalf("got %v, want exit status 255", err)
	}
}

// When ssh itself refuses the key, the error says so and names the entry
// the way known_hosts files it.
func TestRealSSHRefusalIsReportedWithItsEntry(t *testing.T) {
	knownHosts := realSSHHome(t)
	port := localSSHServer(t, "", 0)

	_, otherKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	otherSigner, err := gossh.NewSignerFromKey(otherKey)
	if err != nil {
		t.Fatal(err)
	}

	entry := "[127.0.0.1]:" + strconv.Itoa(port)

	err = os.WriteFile(knownHosts, []byte(knownhosts.Line([]string{entry}, otherSigner.PublicKey())+"\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	// The bastion is spelled the way it was dialled, with its port.
	err = executeSSH(context.Background(), localSSHCommand(t, port), entry)
	if !errors.Is(err, bastionssh.ErrHostKeyMismatch) {
		t.Fatalf("got %v, want an error matching ErrHostKeyMismatch", err)
	}

	if want := "ssh-keygen -R '" + entry + "' -f " + knownHosts; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err, want)
	}
}
