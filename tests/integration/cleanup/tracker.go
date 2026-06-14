// Package cleanup provides ResourceTracker for best-effort reverse-order
// teardown of CPI-managed resources created during integration test runs.
// Designed for use with testing.T.Cleanup or an explicit Cleanup call.
package cleanup

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
)

// ResourceKind identifies the category of a tracked resource. Each kind maps
// to a distinct CPI delete method in the caller-supplied callback table.
type ResourceKind int

const (
	// KindVM maps to the CPI delete_vm method.
	KindVM ResourceKind = iota
	// KindDisk maps to the CPI delete_disk method.
	KindDisk
	// KindSnapshot maps to the CPI delete_snapshot method.
	KindSnapshot
	// KindStemcell maps to the CPI delete_stemcell method.
	KindStemcell
	// KindVNet maps to the CPI delete_network method (SDN vnet).
	KindVNet
	// KindZone is reserved for zone-level resources; no standard CPI method.
	KindZone
	// KindNetwork maps to the CPI delete_network method (bridge variant).
	KindNetwork
)

// kindName returns a human-readable label for logging.
func kindName(k ResourceKind) string {
	switch k {
	case KindVM:
		return "vm"
	case KindDisk:
		return "disk"
	case KindSnapshot:
		return "snapshot"
	case KindStemcell:
		return "stemcell"
	case KindVNet:
		return "vnet"
	case KindZone:
		return "zone"
	case KindNetwork:
		return "network"
	default:
		return fmt.Sprintf("unknown(%d)", int(k))
	}
}

// Resource describes a single tracked resource.
type Resource struct {
	// Kind is the resource category (KindVM, KindDisk, etc.).
	Kind ResourceKind
	// CID is the opaque resource identifier (VMID string, disk CID, snapshot
	// name, stemcell CID, vnet ID, etc.).
	CID string
	// Meta holds arbitrary key/value context per resource (deployment name,
	// environment tag, storage pool, etc.). May be nil.
	Meta map[string]string
}

// Tracker records resources in creation order and tears them down in reverse
// order on Cleanup. All methods are safe for concurrent use.
type Tracker struct {
	mu        sync.Mutex
	resources []Resource
}

// New returns an initialised, empty Tracker.
func New() *Tracker {
	return &Tracker{}
}

// Track appends r to the tracker. Resources are cleaned up in the reverse of
// the order they were tracked (LIFO). Track is safe to call from multiple
// goroutines.
//
// Resources with an empty CID are recorded but skipped during cleanup (the
// callback is never invoked for them). This allows callers to Track after a
// successful create without branching on CID emptiness.
func (t *Tracker) Track(r Resource) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.resources = append(t.resources, r)
}

// Cleanup iterates tracked resources in reverse order and invokes the
// corresponding callback from the callbacks map. Each callback receives a
// context and the Resource; its error is collected but does not stop
// subsequent cleanups (best-effort).
//
// Resources with an empty CID are silently skipped. Resources whose Kind has
// no entry in callbacks are skipped with a log message (no panic).
//
// The combined error returned is nil when every invoked callback succeeded.
// When one or more callbacks returned non-nil errors, the combined error
// wraps all of them via errors.Join.
//
// The caller is responsible for providing a context with an appropriate
// deadline. Cleanup itself does not impose a timeout.
func (t *Tracker) Cleanup(ctx context.Context, callbacks map[ResourceKind]func(ctx context.Context, r Resource) error) error {
	if ctx == nil {
		return errors.New("cleanup: nil context")
	}

	t.mu.Lock()
	// Snapshot the slice under the lock so Track calls during cleanup do not
	// affect the iteration set.
	snapshot := make([]Resource, len(t.resources))
	copy(snapshot, t.resources)
	t.mu.Unlock()

	var errs []error

	for i := len(snapshot) - 1; i >= 0; i-- {
		r := snapshot[i]

		if r.CID == "" {
			log.Printf("cleanup: skip %s with empty CID", kindName(r.Kind))

			continue
		}

		cb, ok := callbacks[r.Kind]
		if !ok {
			log.Printf("cleanup: no callback for kind %s (cid=%q) — skipping", kindName(r.Kind), r.CID)

			continue
		}

		err := cb(ctx, r)
		if err != nil {
			log.Printf("cleanup: %s cid=%q: %v", kindName(r.Kind), r.CID, err)
			errs = append(errs, fmt.Errorf("%s cid=%q: %w", kindName(r.Kind), r.CID, err))
		} else {
			log.Printf("cleanup: %s cid=%q: ok", kindName(r.Kind), r.CID)
		}
	}

	return errors.Join(errs...)
}
