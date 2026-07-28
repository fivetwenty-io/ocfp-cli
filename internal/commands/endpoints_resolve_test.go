package commands

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// fakeHostResolver is a test double for hostResolver. Each host maps to a
// canned response (addresses or error); an optional delay lets tests exercise
// resolveTimeout without touching a real network.
type fakeHostResolver struct {
	mu        sync.Mutex
	calls     []string
	responses map[string][]string
	errs      map[string]error
	delay     time.Duration
}

func newFakeHostResolver() *fakeHostResolver {
	return &fakeHostResolver{
		responses: make(map[string][]string),
		errs:      make(map[string]error),
	}
}

// LookupHost implements hostResolver. It records the call, then either
// blocks until f.delay elapses or ctx is done (whichever comes first) before
// returning the registered response for host.
func (f *fakeHostResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, host)
	f.mu.Unlock()

	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if err, ok := f.errs[host]; ok {
		return nil, err
	}

	return f.responses[host], nil
}

// callCount returns how many times LookupHost was invoked, safe for
// concurrent access from resolveAll's goroutines.
func (f *fakeHostResolver) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.calls)
}

// installFakeHostResolver swaps endpointsResolver for fake and returns a
// restore func, mirroring installFakeRunner's shape for commandRunner.
func installFakeHostResolver(fake *fakeHostResolver) func() {
	orig := endpointsResolver
	endpointsResolver = fake

	return func() { endpointsResolver = orig }
}

// TestResolveAll_NoResolveSkipsNetwork verifies that noResolve=true never
// invokes the resolver and always returns an empty result map, regardless of
// how many hosts are passed in.
func TestResolveAll_NoResolveSkipsNetwork(t *testing.T) {
	// Not t.Parallel(): this test swaps the package-level endpointsResolver
	// seam, same convention as installFakeRunner's callers for commandRunner.
	fake := newFakeHostResolver()
	fake.responses["bastion.example.lab.internal"] = []string{"10.0.0.5"}

	restore := installFakeHostResolver(fake)
	defer restore()

	result := resolveAll(t.Context(), []string{"bastion.example.lab.internal"}, true)

	assert.Empty(t, result)
	assert.Equal(t, 0, fake.callCount())
}

// TestResolveAll_ReturnsFirstAddress verifies that when LookupHost returns
// multiple addresses for a host, resolveAll's result map holds the first one.
func TestResolveAll_ReturnsFirstAddress(t *testing.T) {
	// Not t.Parallel(): swaps the package-level endpointsResolver seam.
	fake := newFakeHostResolver()
	fake.responses["multi.example.lab.internal"] = []string{"10.0.0.7", "10.0.0.8"}

	restore := installFakeHostResolver(fake)
	defer restore()

	result := resolveAll(t.Context(), []string{"multi.example.lab.internal"}, false)

	assert.Equal(t, "10.0.0.7", result["multi.example.lab.internal"])
	assert.Equal(t, 1, fake.callCount())
}

// TestResolveAll_SkipsEmptyAndWildcardHosts verifies resolveAll never hands
// an empty string or a wildcard record name (containing "*") to the
// resolver, and neither key appears in the result map.
func TestResolveAll_SkipsEmptyAndWildcardHosts(t *testing.T) {
	// Not t.Parallel(): swaps the package-level endpointsResolver seam.
	fake := newFakeHostResolver()
	fake.responses["real.example.lab.internal"] = []string{"10.0.0.9"}

	restore := installFakeHostResolver(fake)
	defer restore()

	hosts := []string{"", "*.apps.example.lab.internal", "real.example.lab.internal"}
	result := resolveAll(t.Context(), hosts, false)

	assert.Equal(t, "10.0.0.9", result["real.example.lab.internal"])
	assert.NotContains(t, result, "")
	assert.NotContains(t, result, "*.apps.example.lab.internal")
	assert.Equal(t, 1, fake.callCount())
}

// TestResolveAll_HonorsTimeout verifies that a lookup exceeding resolveTimeout
// is abandoned promptly (well before the fake resolver's artificial delay
// elapses) and yields no entry for that host, without resolveAll returning
// an error.
func TestResolveAll_HonorsTimeout(t *testing.T) {
	// Not t.Parallel(): swaps both package-level seams (resolveTimeout,
	// endpointsResolver).
	origTimeout := resolveTimeout
	resolveTimeout = 20 * time.Millisecond

	defer func() { resolveTimeout = origTimeout }()

	fake := newFakeHostResolver()
	fake.delay = 500 * time.Millisecond

	restore := installFakeHostResolver(fake)
	defer restore()

	start := time.Now()
	result := resolveAll(t.Context(), []string{"slow.example.lab.internal"}, false)
	elapsed := time.Since(start)

	assert.NotContains(t, result, "slow.example.lab.internal")
	assert.Less(t, elapsed, 200*time.Millisecond, "resolveAll should abandon the lookup at resolveTimeout, not wait for the fake's full delay")
}
