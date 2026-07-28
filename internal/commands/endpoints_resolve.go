package commands

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// hostResolver abstracts DNS lookups for testing. Production code uses
// *net.Resolver's LookupHost method via endpointsResolver; tests inject a
// fake so no real network traffic ever occurs.
type hostResolver interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// endpointsResolver is the package-level hostResolver used by resolveAll.
// Tests replace this with a fake to avoid real DNS lookups.
var endpointsResolver hostResolver = &net.Resolver{} //nolint:gochecknoglobals // test seam, mirrors runner

// resolveTimeout bounds each individual lookup. It is a package var so tests
// can shrink it to exercise timeout behavior without waiting seconds.
var resolveTimeout = 3 * time.Second //nolint:gochecknoglobals // test seam: shrunk in timeout tests

// resolveConcurrency bounds how many lookups run at once, protecting against
// a bloc with many rows firing an unbounded burst of concurrent DNS queries
// (R-03).
const resolveConcurrency = 8

// resolveAll resolves each unique, non-empty, non-wildcard host in hosts to
// its first returned address, applying resolveTimeout per lookup and running
// at most resolveConcurrency lookups concurrently.
//
// Under noResolve, no lookups occur at all and an empty map is returned —
// this is the --no-resolve escape hatch (R-03): callers render every
// RESOLVED IP cell blank by treating a missing map entry as blank, so this
// function does not need a separate "skip" return shape.
//
// A host that is empty, contains "*" (a wildcard record name that cannot be
// looked up), fails to resolve, times out, or returns zero addresses is
// simply absent from the result map — resolveAll never returns an error.
// DNS lookup failure is an expected, common outcome for this command (a
// bloc's hostname may not exist in DNS yet, or ever), not an exceptional
// condition the caller needs to handle separately from a blank result.
func resolveAll(ctx context.Context, hosts []string, noResolve bool) map[string]string {
	results := make(map[string]string)
	if noResolve {
		return results
	}

	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(resolveConcurrency)

	var mu sync.Mutex

	seen := make(map[string]bool, len(hosts))

	for _, host := range hosts {
		if host == "" || strings.Contains(host, "*") || seen[host] {
			continue
		}

		seen[host] = true

		group.Go(func() error {
			lookupCtx, cancel := context.WithTimeout(groupCtx, resolveTimeout)
			defer cancel()

			addrs, err := endpointsResolver.LookupHost(lookupCtx, host)
			if err != nil || len(addrs) == 0 {
				return nil
			}

			mu.Lock()
			results[host] = addrs[0]
			mu.Unlock()

			return nil
		})
	}

	// Every goroutine above always returns nil: lookup failures are recorded
	// by simply omitting the map entry, never by propagating an error, per
	// this function's own no-error contract. Wait() is called only to block
	// until all lookups finish before returning results.
	_ = group.Wait()

	return results
}
