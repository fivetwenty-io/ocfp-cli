// Package capacity resolves PVE cloud-config capacity values (workers,
// cf_max_in_flight) using a three-level priority chain per Decision D1:
//
//  1. Explicit operator config (Config.CfMaxInFlight / Config.Workers)
//  2. Live PVE node status query (CPUCount clamped to [4,16])
//  3. Hardcoded lab defaults (MaxInFlight=12, Workers=4)
package capacity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const (
	// DefaultMaxInFlight is the lab-default cf_max_in_flight value, sized to
	// PVE's default storage worker thread count. Sized per R3-08: a 12-worker
	// PVE node can service 12 concurrent create_vm calls before storage-lock
	// contention dominates.
	DefaultMaxInFlight = 12

	// DefaultWorkers is the lab-default BOSH compilation workers value.
	// Sized to the Wayne-lab compilation vm_type (4 cores) so compilation VMs
	// are fully utilized without oversubscribing a single PVE host.
	DefaultWorkers = 4

	// MinMaxInFlight is the lower clamp applied to CPU-count-derived values.
	// Prevents single-CPU nodes from producing an unusably low concurrency cap.
	MinMaxInFlight = 4

	// MaxMaxInFlight is the upper clamp applied to CPU-count-derived values.
	// Prevents large-core hosts from generating lock-storm levels of concurrency.
	MaxMaxInFlight = 16
)

// SourceKind documents where a resolved value originated.
type SourceKind string

const (
	// SourceConfig means the value came from explicit operator configuration.
	SourceConfig SourceKind = "config"
	// SourceQuery means the value was derived from a live PVE node status query.
	SourceQuery SourceKind = "query"
	// SourceDefault means the hardcoded lab default was used (query failed or skipped).
	SourceDefault SourceKind = "default"
)

// NodeStatus holds the subset of PVE /nodes/{node}/status data used for
// capacity resolution.
type NodeStatus struct {
	// CPUCount is the total number of logical CPUs on the node.
	CPUCount int
	// MemoryBytes is total RAM in bytes.
	MemoryBytes int64
	// LoadAvg is the 1/5/15-minute load average (may be empty if unavailable).
	LoadAvg []float64
}

// Querier retrieves live node status from a PVE cluster.
// Accepts a context for deadline/cancellation propagation.
//
// Implementations must be safe for concurrent use.
type Querier interface {
	NodeStatus(ctx context.Context, node string) (*NodeStatus, error)
}

// HTTPClient is the transport used by RESTQuerier. Accepts the standard
// *http.Client or any compatible implementation (useful for testing).
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// RESTQuerier implements Querier via the PVE REST API v2.
// It hits GET /api2/json/nodes/{node}/status and parses the response.
type RESTQuerier struct {
	// Client is the HTTP transport. When nil, http.DefaultClient is used.
	Client HTTPClient
	// Base is the API base URL, e.g. "https://pve:8006/api2/json".
	// Must not end with a slash.
	Base string
	// Auth is the verbatim Authorization header value, e.g.
	// "PVEAPIToken=user@realm!token=secret".
	Auth string
}

// pveNodeStatusResponse mirrors the PVE REST API envelope.
//
//	{"data": {"cpuinfo": {"cpus": 12, "cores": 12, ...}, "memory": {"total": ...}, ...}}
type pveNodeStatusResponse struct {
	Data struct {
		CPUInfo struct {
			CPUs  int `json:"cpus"`
			Cores int `json:"cores"`
		} `json:"cpuinfo"`
		Memory struct {
			Total int64 `json:"total"`
		} `json:"memory"`
		LoadAvg []float64 `json:"loadavg"`
	} `json:"data"`
}

// NodeStatus fetches /nodes/{node}/status and returns parsed capacity data.
//
// Failure modes:
//   - node empty: returns error immediately.
//   - HTTP error or non-200: returns wrapped error; caller falls back to default.
//   - Body read failure: returns wrapped error.
//   - JSON parse failure: returns wrapped error.
//   - CPUCount == 0 after parse (PVE returned unexpected shape): returns error so
//     caller can fall through to default rather than returning zero-value capacity.
func (q *RESTQuerier) NodeStatus(ctx context.Context, node string) (*NodeStatus, error) {
	if node == "" {
		return nil, errors.New("pve/capacity: node name must not be empty") //nolint:err113 // descriptive error, not caller-testable
	}

	url := fmt.Sprintf("%s/nodes/%s/status", q.Base, node)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("pve/capacity: build request: %w", err)
	}

	if q.Auth != "" {
		req.Header.Set("Authorization", q.Auth)
	}

	client := q.Client
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("pve/capacity: HTTP GET %s: %w", url, err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pve/capacity: HTTP GET %s: unexpected status %d", url, resp.StatusCode) //nolint:err113 // descriptive error, not caller-testable
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("pve/capacity: read response body: %w", err)
	}

	var parsed pveNodeStatusResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("pve/capacity: parse response JSON: %w", err)
	}

	// PVE reports logical CPUs in cpuinfo.cpus; fall back to cores if cpus is zero.
	cpuCount := parsed.Data.CPUInfo.CPUs
	if cpuCount == 0 {
		cpuCount = parsed.Data.CPUInfo.Cores
	}

	if cpuCount == 0 {
		return nil, fmt.Errorf("pve/capacity: node %q returned zero CPU count; cannot derive capacity", node) //nolint:err113 // descriptive error, not caller-testable
	}

	return &NodeStatus{
		CPUCount:    cpuCount,
		MemoryBytes: parsed.Data.Memory.Total,
		LoadAvg:     parsed.Data.LoadAvg,
	}, nil
}

// Resolved carries the fully-resolved capacity values and the source that
// produced them.
type Resolved struct {
	// Workers is the BOSH compilation workers count.
	Workers int
	// MaxInFlight is the cf_max_in_flight value for the cloud-config update block.
	MaxInFlight int
	// Source documents which resolution level produced these values.
	Source SourceKind
}

// Resolve applies the D1 three-level resolution chain for capacity values:
//
//  1. If cfgMaxInFlight > 0 and cfgWorkers > 0: use them; Source = "config".
//  2. If cfgMaxInFlight > 0 but cfgWorkers == 0: config wins for MaxInFlight,
//     query/default supplies Workers. Source = "config".
//  3. If cfgMaxInFlight == 0: attempt live query. If node status returns a
//     CPUCount > 0, clamp(CPUCount, 4, 16) → MaxInFlight. Workers = clamp(CPUCount/2, 2, 8).
//     Source = "query".
//  4. Query missing, nil, or returns error: use DefaultMaxInFlight / DefaultWorkers.
//     Source = "default".
//
// q may be nil; when nil, the query step is skipped (equivalent to query error).
// node is ignored when q is nil.
func Resolve(ctx context.Context, q Querier, node string, cfgWorkers, cfgMaxInFlight int) Resolved {
	// Config wins when both fields are set.
	if cfgMaxInFlight > 0 && cfgWorkers > 0 {
		return Resolved{
			Workers:     cfgWorkers,
			MaxInFlight: cfgMaxInFlight,
			Source:      SourceConfig,
		}
	}

	// Config wins for MaxInFlight even if Workers is unset.
	if cfgMaxInFlight > 0 {
		workers := cfgWorkers
		if workers <= 0 {
			workers = DefaultWorkers
		}
		return Resolved{
			Workers:     workers,
			MaxInFlight: cfgMaxInFlight,
			Source:      SourceConfig,
		}
	}

	// Workers explicitly set but MaxInFlight not — honour Workers from config
	// and derive MaxInFlight from query / default.
	explicitWorkers := cfgWorkers > 0

	// Try live query.
	if q != nil && node != "" {
		status, err := q.NodeStatus(ctx, node)
		if err == nil && status != nil && status.CPUCount > 0 {
			maxInFlight := clamp(status.CPUCount, MinMaxInFlight, MaxMaxInFlight)

			workers := cfgWorkers
			if !explicitWorkers {
				// Derive workers: half of CPU count, clamped [2,8].
				workers = clamp(status.CPUCount/2, 2, 8) //nolint:mnd // range constant, not magic
			}

			return Resolved{
				Workers:     workers,
				MaxInFlight: maxInFlight,
				Source:      SourceQuery,
			}
		}
	}

	// Default fallback.
	workers := cfgWorkers
	if workers <= 0 {
		workers = DefaultWorkers
	}

	return Resolved{
		Workers:     workers,
		MaxInFlight: DefaultMaxInFlight,
		Source:      SourceDefault,
	}
}

// clamp returns v clamped to [lo, hi]. lo must be ≤ hi.
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}

	if v > hi {
		return hi
	}

	return v
}
