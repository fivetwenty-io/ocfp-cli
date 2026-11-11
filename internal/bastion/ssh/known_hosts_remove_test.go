package ssh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func authorizedKey(key gossh.PublicKey) string {
	return strings.TrimSpace(string(gossh.MarshalAuthorizedKey(key)))
}

func writeRaw(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "known_hosts")

	err := os.WriteFile(path, []byte(content), mode)
	if err != nil {
		t.Fatal(err)
	}

	return path
}

func TestRemoveKnownHostDropsEveryFormForTheHost(t *testing.T) {
	t.Parallel()

	plain := newEd25519Signer(t).PublicKey()
	bracketed := newEd25519Signer(t).PublicKey()
	hashed := newECDSASigner(t).PublicKey()
	listed := newEd25519Signer(t).PublicKey()
	other := newEd25519Signer(t).PublicKey()

	keep := strings.Join([]string{
		"# comment stays exactly as written  ",
		"",
		"10.0.0.50 " + authorizedKey(other),
		"  10.0.0.6\t" + authorizedKey(other) + " trailing comment",
		"@cert-authority 10.0.0.5 " + authorizedKey(other),
		"@revoked 10.0.0.5 " + authorizedKey(other),
		"10.0.0.* " + authorizedKey(other),
		knownhosts.HashHostname("10.0.0.6") + " " + authorizedKey(other) + "\r",
	}, "\n")

	content := strings.Join([]string{
		"10.0.0.5 " + authorizedKey(plain),
		keep,
		"[10.0.0.5]:2222 " + authorizedKey(bracketed),
		knownhosts.HashHostname("10.0.0.5") + " " + authorizedKey(hashed),
		"bastion.example,10.0.0.5 " + authorizedKey(listed),
	}, "\n") + "\n"

	path := writeRaw(t, content, 0o640)

	removed, err := RemoveKnownHost(path, "10.0.0.5", DefaultSSHPort)
	if err != nil {
		t.Fatalf("RemoveKnownHost: %v", err)
	}

	if got := readFile(t, path); got != keep+"\n" {
		t.Errorf("remaining file:\n%q\nwant:\n%q", got, keep+"\n")
	}

	want := map[string]bool{
		gossh.FingerprintSHA256(plain):     true,
		gossh.FingerprintSHA256(bracketed): true,
		gossh.FingerprintSHA256(hashed):    true,
		gossh.FingerprintSHA256(listed):    true,
	}

	if len(removed) != len(want) {
		t.Fatalf("removed %d entries %+v, want %d", len(removed), removed, len(want))
	}

	for _, entry := range removed {
		if !want[entry.Fingerprint] {
			t.Errorf("unexpected removed entry %+v", entry)
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o640 {
		t.Errorf("file mode = %v, want 0640 preserved", info.Mode().Perm())
	}
}

func TestRemoveKnownHostMatchesHashedEntryOnAPort(t *testing.T) {
	t.Parallel()

	key := newEd25519Signer(t).PublicKey()
	path := writeRaw(t, knownhosts.HashHostname("[bastion.example]:2222")+" "+authorizedKey(key)+"\n", 0o600)

	removed, err := RemoveKnownHost(path, "bastion.example", 2222)
	if err != nil {
		t.Fatalf("RemoveKnownHost: %v", err)
	}

	if len(removed) != 1 || removed[0].Fingerprint != gossh.FingerprintSHA256(key) {
		t.Errorf("removed = %+v, want the one hashed entry", removed)
	}

	if got := readFile(t, path); got != "" {
		t.Errorf("remaining file = %q, want empty", got)
	}
}

func TestRemoveKnownHostLeavesFileUntouchedWithoutAMatch(t *testing.T) {
	t.Parallel()

	content := "10.0.0.6 " + authorizedKey(newEd25519Signer(t).PublicKey()) + "\nno newline at end"
	path := writeRaw(t, content, 0o600)

	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveKnownHost(path, "10.0.0.5", DefaultSSHPort)
	if err != nil {
		t.Fatalf("RemoveKnownHost: %v", err)
	}

	if len(removed) != 0 {
		t.Errorf("removed = %+v, want none", removed)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if !after.ModTime().Equal(before.ModTime()) || readFile(t, path) != content {
		t.Error("file was rewritten although nothing matched")
	}
}

func TestRemoveKnownHostToleratesMissingFile(t *testing.T) {
	t.Parallel()

	removed, err := RemoveKnownHost(filepath.Join(t.TempDir(), "known_hosts"), "10.0.0.5", DefaultSSHPort)
	if err != nil || len(removed) != 0 {
		t.Errorf("RemoveKnownHost on a missing file = %+v, %v; want nothing and no error", removed, err)
	}
}

func TestRemoveKnownHostRejectsEmptyHost(t *testing.T) {
	t.Parallel()

	_, err := RemoveKnownHost(writeRaw(t, "", 0o600), "  ", DefaultSSHPort)
	if err == nil {
		t.Error("RemoveKnownHost accepted an empty host")
	}
}

// A negated name that covers the host means the line does not apply to it,
// as OpenSSH reads it, whether the negation is a literal name or a pattern.
// Such a line stays. A negation that names some other host does not protect
// the line.
func TestRemoveKnownHostKeepsLinesNegatingTheHost(t *testing.T) {
	t.Parallel()

	key := newEd25519Signer(t).PublicKey()
	other := newEd25519Signer(t).PublicKey()

	keep := strings.Join([]string{
		"bastion.example,!bastion.example " + authorizedKey(other),
		"bastion.example,!BASTION.example " + authorizedKey(other),
		"bastion.example,!bastion.* " + authorizedKey(other),
		"bastion.example,!bastion.exampl? " + authorizedKey(other),
	}, "\n") + "\n"

	path := writeRaw(t, keep+"bastion.example,!other.example "+authorizedKey(key)+"\n", 0o600)

	removed, err := RemoveKnownHost(path, "bastion.example", DefaultSSHPort)
	if err != nil {
		t.Fatalf("RemoveKnownHost: %v", err)
	}

	if len(removed) != 1 || removed[0].Fingerprint != gossh.FingerprintSHA256(key) {
		t.Errorf("removed = %+v, want only the line whose negation names another host", removed)
	}

	if got := readFile(t, path); got != keep {
		t.Errorf("remaining file:\n%q\nwant:\n%q", got, keep)
	}
}

// OpenSSH lowercases a name before it hashes it, so a host given in capitals
// must still find its hashed entry, on the default port and on another one.
func TestRemoveKnownHostMatchesHashedEntryForUppercaseHost(t *testing.T) {
	t.Parallel()

	plain := newEd25519Signer(t).PublicKey()
	ported := newEd25519Signer(t).PublicKey()

	content := knownhosts.HashHostname("bastion.example") + " " + authorizedKey(plain) + "\n" +
		knownhosts.HashHostname("[bastion.example]:2222") + " " + authorizedKey(ported) + "\n"
	path := writeRaw(t, content, 0o600)

	removed, err := RemoveKnownHost(path, "Bastion.EXAMPLE", 2222)
	if err != nil {
		t.Fatalf("RemoveKnownHost: %v", err)
	}

	if len(removed) != 2 {
		t.Errorf("removed = %+v, want both hashed entries", removed)
	}

	if got := readFile(t, path); got != "" {
		t.Errorf("remaining file = %q, want empty", got)
	}
}

// A known_hosts kept elsewhere and linked into ~/.ssh must stay a link. The
// rewrite lands on the file it points at, which keeps its mode.
func TestRemoveKnownHostWritesThroughASymlink(t *testing.T) {
	t.Parallel()

	keep := "10.0.0.6 " + authorizedKey(newEd25519Signer(t).PublicKey()) + "\n"
	target := writeRaw(t, "10.0.0.5 "+authorizedKey(newEd25519Signer(t).PublicKey())+"\n"+keep, 0o640)

	link := filepath.Join(t.TempDir(), "known_hosts")

	err := os.Symlink(target, link)
	if err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveKnownHost(link, "10.0.0.5", DefaultSSHPort)
	if err != nil {
		t.Fatalf("RemoveKnownHost: %v", err)
	}

	if len(removed) != 1 {
		t.Errorf("removed = %+v, want one entry", removed)
	}

	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is no longer a symlink (mode %v)", link, info.Mode())
	}

	if got := readFile(t, target); got != keep {
		t.Errorf("link target = %q, want %q", got, keep)
	}

	targetInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}

	if targetInfo.Mode().Perm() != 0o640 {
		t.Errorf("target mode = %v, want 0640 preserved", targetInfo.Mode().Perm())
	}
}
