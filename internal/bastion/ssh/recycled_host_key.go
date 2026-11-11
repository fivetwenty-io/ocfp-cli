package ssh

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// DefaultRecycledHostKeyTimeout is how long Connect waits, in recycle mode,
// for a host to present the key it held before its root disk was replaced.
const DefaultRecycledHostKeyTimeout = 10 * time.Minute

// ErrRecycledHostKeyNotRestored reports a recycled host that kept presenting
// keys it did not hold before the recycle until the wait ran out.
var ErrRecycledHostKeyNotRestored = errors.New("recycled host did not present its previous host key")

// ErrNoPreviousHostKey reports a host put in recycle mode with no host key
// on record from before the recycle. There is nothing to check its key
// against, so Connect refuses to dial it rather than trust a new key.
var ErrNoPreviousHostKey = errors.New("no host key from before the recycle is on record for this host")

// errTemporaryHostKey refuses one handshake with a recycled host that has
// not yet restored its previous host key. Connect retries it until the wait
// runs out, and the key is never recorded.
var errTemporaryHostKey = errors.New("recycled host presented a key it did not hold before the recycle")

// RecycledHostKeyError describes a recycled host that never presented its
// previous host key. errors.Is matches it against
// ErrRecycledHostKeyNotRestored.
type RecycledHostKeyError struct {
	// Host is the name known_hosts files the host under.
	Host string
	// Expected lists the fingerprints the host held before the recycle.
	Expected []string
	// Presented lists the fingerprints the host offered instead.
	Presented []string
	// Timeout is how long Connect waited.
	Timeout time.Duration
}

// Error names both sets of keys and what to check before trusting a new one.
func (e *RecycledHostKeyError) Error() string {
	return fmt.Sprintf(
		"%s did not present its previous host key within %s: it presented %s, but before the recycle it held %s. "+
			"The bastion may still be restoring its host keys from the data disk, it may have lost them, or someone "+
			"may be intercepting the connection. No key was recorded. Compare the fingerprints in the bastion's "+
			"console log with the ones above before trusting a new key",
		e.Host, e.Timeout, strings.Join(e.Presented, ", "), strings.Join(e.Expected, ", "))
}

// Is reports whether target is ErrRecycledHostKeyNotRestored.
func (e *RecycledHostKeyError) Is(target error) bool {
	return target == ErrRecycledHostKeyNotRestored
}

// recycledHostKeys holds what recycle mode expects of a host and what the
// host has offered so far. The host key callback runs on the handshake's
// goroutine, so the offered keys are guarded.
type recycledHostKeys struct {
	keys    []ssh.PublicKey
	timeout time.Duration

	mu        sync.Mutex
	presented []string
}

func (r *recycledHostKeys) expects(key ssh.PublicKey) bool {
	marshalled := key.Marshal()

	return slices.ContainsFunc(r.keys, func(want ssh.PublicKey) bool {
		return bytes.Equal(want.Marshal(), marshalled)
	})
}

func (r *recycledHostKeys) sawOther(key ssh.PublicKey) {
	r.mu.Lock()
	defer r.mu.Unlock()

	fingerprint := describeKey(key)
	if !slices.Contains(r.presented, fingerprint) {
		r.presented = append(r.presented, fingerprint)
	}
}

// waiting reports whether the host has offered a key other than the
// expected ones, which means it is up and still restoring its own.
func (r *recycledHostKeys) waiting() bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.presented) > 0
}

func (r *recycledHostKeys) offered() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.presented)
}

func (r *recycledHostKeys) fingerprints() []string {
	fingerprints := make([]string, 0, len(r.keys))
	for _, key := range r.keys {
		fingerprints = append(fingerprints, describeKey(key))
	}

	return fingerprints
}

// algorithms lists only the host key algorithms the expected keys use, so
// the host is asked for one of them. Nil keeps the library default.
func (r *recycledHostKeys) algorithms() []string {
	supported := ssh.SupportedAlgorithms().HostKeys

	var algorithms []string

	for _, key := range r.keys {
		for _, algo := range algorithmsForKeyType(key.Type()) {
			if slices.Contains(supported, algo) && !slices.Contains(algorithms, algo) {
				algorithms = append(algorithms, algo)
			}
		}
	}

	return algorithms
}

func describeKey(key ssh.PublicKey) string {
	return key.Type() + " " + ssh.FingerprintSHA256(key)
}

// ExpectRecycledHostKeys puts the client in recycle mode for a host whose
// root disk was just replaced. Such a host boots with keys its new root disk
// generated and presents them until it restores its own from its data disk.
//
// In recycle mode the host key check accepts only keys, which should be the
// ones the host presented before the recycle. Any other key fails that
// handshake and is never recorded, and Connect keeps retrying until the
// host presents an expected key or timeout passes, when it returns a
// RecycledHostKeyError. An expected key is recorded under the address
// Connect dials. If that address holds a different key, the entry is stale,
// so its lines are replaced with the expected key and both fingerprints are
// logged. The external ssh fallback is not used, since it applies its own
// known_hosts rules.
//
// A timeout of zero or less means DefaultRecycledHostKeyTimeout. With no
// keys there is nothing to check the host against, so Connect fails at once
// with ErrNoPreviousHostKey, without dialing or recording anything.
func (c *Client) ExpectRecycledHostKeys(keys []ssh.PublicKey, timeout time.Duration) {
	if timeout <= 0 {
		timeout = DefaultRecycledHostKeyTimeout
	}

	c.recycled = &recycledHostKeys{keys: slices.Clone(keys), timeout: timeout}
}

// noPreviousHostKey returns the refusal for a client in recycle mode with
// no key to check the host against, or nil.
func (c *Client) noPreviousHostKey() error {
	if c.recycled == nil || len(c.recycled.keys) > 0 {
		return nil
	}

	c.log.Errorw("Recycled bastion has no host key on record from before the recycle; refusing to connect",
		"host", c.config.Host)

	return fmt.Errorf("failed to connect to %s: %w", c.config.Host, ErrNoPreviousHostKey)
}

// recycledHostKeyCallback is the host key check in recycle mode.
func (c *Client) recycledHostKeyCallback(knownHostsPath string, expected *recycledHostKeys) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		host := knownhosts.Normalize(hostname)

		if !expected.expects(key) {
			expected.sawOther(key)
			c.log.Warnw("Recycled bastion presented a host key it did not hold before the recycle; "+
				"waiting for it to restore its own",
				"host", host,
				"presented", describeKey(key),
				"expected", strings.Join(expected.fingerprints(), ", "))

			return fmt.Errorf("%w: %s presented %s", errTemporaryHostKey, host, describeKey(key))
		}

		file, err := loadKnownHosts(knownHostsPath)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrHostKeyRejected, err)
		}

		err = file.check(hostname, remote, key)
		if err == nil {
			return nil
		}

		// The key is the one the host held before the recycle, so a
		// different entry for this address is stale, left by some
		// earlier holder of the address. It gives way to the proven key,
		// so later connections outside recycle mode find it.
		if mismatch := mismatchFromKeyError(err, knownHostsPath, hostname, key); mismatch != nil {
			return c.replaceStaleHostKey(knownHostsPath, hostname, key, mismatch)
		}

		return c.handleHostKeyError(err, knownHostsPath, hostname, key)
	}
}

// replaceStaleHostKey records the proven key for hostname in place of the
// stale entries known_hosts holds for that address, and logs both sets of
// fingerprints.
func (c *Client) replaceStaleHostKey(knownHostsPath, hostname string, key ssh.PublicKey, mismatch *HostKeyMismatchError) error {
	removed, err := replaceKnownHostKey(knownHostsPath, hostname, key)
	if err != nil {
		return fmt.Errorf("failed to replace stale host key for %s: %w", mismatch.Host, err)
	}

	old := make([]string, 0, len(removed))
	for _, entry := range removed {
		old = append(old, fmt.Sprintf("%s (line %d)", entry.Fingerprint, entry.Line))
	}

	c.log.Warnw("Recycled bastion presented its previous host key, but known_hosts recorded another key "+
		"for this address; replaced the stale entry with the previous key",
		"host", mismatch.Host,
		"known_hosts", knownHostsPath,
		"old", strings.Join(old, ", "),
		"recorded", strings.Join(mismatch.Recorded, ", "),
		"new", describeKey(key))

	return nil
}

// recycledKeyTimeout returns the error for a recycle-mode wait that ran
// out, or nil when Connect is not waiting on a recycled key or its caller
// cancelled.
func (c *Client) recycledKeyTimeout(callerCancelled bool) error {
	if c.recycled == nil || callerCancelled || !c.recycled.waiting() {
		return nil
	}

	port := c.config.Port
	if port == 0 {
		port = DefaultSSHPort
	}

	return &RecycledHostKeyError{
		Host:      knownhosts.Normalize(net.JoinHostPort(c.config.Host, strconv.Itoa(port))),
		Expected:  c.recycled.fingerprints(),
		Presented: c.recycled.offered(),
		Timeout:   c.recycled.timeout,
	}
}
