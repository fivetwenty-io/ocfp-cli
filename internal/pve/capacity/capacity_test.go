package capacity_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/pve/capacity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Resolve — unit tests
// ---------------------------------------------------------------------------

// TestResolve_ConfigWins verifies that when cfgMaxInFlight > 0 the config
// value wins regardless of the querier result.
func TestResolve_ConfigWins(t *testing.T) {
	t.Parallel()

	// Querier that would return CPUCount=8 if called.
	q := &stubQuerier{cpuCount: 8}

	got := capacity.Resolve(context.Background(), q, "pve01", 4, 20)

	assert.Equal(t, 20, got.MaxInFlight, "config MaxInFlight must win")
	assert.Equal(t, 4, got.Workers, "config Workers must win when both set")
	assert.Equal(t, capacity.SourceConfig, got.Source)
	assert.False(t, q.called, "querier must not be invoked when config supplies both fields")
}

// TestResolve_ConfigWins_MaxInFlightOnlyWorkerDefault verifies that when only
// cfgMaxInFlight is set (cfgWorkers == 0), Source is still "config" and
// Workers falls back to DefaultWorkers.
func TestResolve_ConfigWins_MaxInFlightOnlyWorkerDefault(t *testing.T) {
	t.Parallel()

	got := capacity.Resolve(context.Background(), nil, "", 0, 20)

	assert.Equal(t, 20, got.MaxInFlight)
	assert.Equal(t, capacity.DefaultWorkers, got.Workers)
	assert.Equal(t, capacity.SourceConfig, got.Source)
}

// TestResolve_QuerySucceeds_DerivesFromCPUCount verifies that when cfgMaxInFlight
// is 0 and the querier returns CPUCount=8, MaxInFlight=8 (within clamp bounds).
func TestResolve_QuerySucceeds_DerivesFromCPUCount(t *testing.T) {
	t.Parallel()

	q := &stubQuerier{cpuCount: 8}

	got := capacity.Resolve(context.Background(), q, "pve01", 0, 0)

	assert.Equal(t, 8, got.MaxInFlight, "CPUCount 8 should produce MaxInFlight 8 (within clamp)")
	assert.Equal(t, capacity.SourceQuery, got.Source)
	assert.True(t, q.called)
}

// TestResolve_QueryFails_FallsBackToDefault verifies that when the querier
// returns an error, MaxInFlight defaults to DefaultMaxInFlight (12).
func TestResolve_QueryFails_FallsBackToDefault(t *testing.T) {
	t.Parallel()

	q := &stubQuerier{err: fmt.Errorf("connection refused")}

	got := capacity.Resolve(context.Background(), q, "pve01", 0, 0)

	assert.Equal(t, capacity.DefaultMaxInFlight, got.MaxInFlight)
	assert.Equal(t, capacity.DefaultWorkers, got.Workers)
	assert.Equal(t, capacity.SourceDefault, got.Source)
}

// TestResolve_NilQuerier_FallsBackToDefault verifies that a nil querier falls
// straight through to the default.
func TestResolve_NilQuerier_FallsBackToDefault(t *testing.T) {
	t.Parallel()

	got := capacity.Resolve(context.Background(), nil, "pve01", 0, 0)

	assert.Equal(t, capacity.DefaultMaxInFlight, got.MaxInFlight)
	assert.Equal(t, capacity.DefaultWorkers, got.Workers)
	assert.Equal(t, capacity.SourceDefault, got.Source)
}

// TestResolve_QueryReturnsLowCPU_Clamped verifies CPUCount=2 is clamped to
// MinMaxInFlight (4).
func TestResolve_QueryReturnsLowCPU_Clamped(t *testing.T) {
	t.Parallel()

	q := &stubQuerier{cpuCount: 2}

	got := capacity.Resolve(context.Background(), q, "pve01", 0, 0)

	assert.Equal(t, capacity.MinMaxInFlight, got.MaxInFlight, "CPUCount 2 must clamp to MinMaxInFlight")
	assert.Equal(t, capacity.SourceQuery, got.Source)
}

// TestResolve_QueryReturnsHighCPU_Clamped verifies CPUCount=64 is clamped to
// MaxMaxInFlight (16).
func TestResolve_QueryReturnsHighCPU_Clamped(t *testing.T) {
	t.Parallel()

	q := &stubQuerier{cpuCount: 64}

	got := capacity.Resolve(context.Background(), q, "pve01", 0, 0)

	assert.Equal(t, capacity.MaxMaxInFlight, got.MaxInFlight, "CPUCount 64 must clamp to MaxMaxInFlight")
	assert.Equal(t, capacity.SourceQuery, got.Source)
}

// TestResolve_ExplicitWorkers_QueryForMaxInFlight verifies that cfgWorkers > 0
// but cfgMaxInFlight == 0 defers MaxInFlight to the querier while honouring
// the explicit Workers value.
func TestResolve_ExplicitWorkers_QueryForMaxInFlight(t *testing.T) {
	t.Parallel()

	q := &stubQuerier{cpuCount: 12}

	got := capacity.Resolve(context.Background(), q, "pve01", 6, 0)

	assert.Equal(t, 6, got.Workers, "explicit Workers must be honoured")
	assert.Equal(t, 12, got.MaxInFlight, "MaxInFlight from CPUCount=12 within clamp")
	assert.Equal(t, capacity.SourceQuery, got.Source)
}

// TestResolve_EmptyNode_SkipsQuery verifies that when node is empty the
// query step is skipped (falls to default) even when a querier is provided.
func TestResolve_EmptyNode_SkipsQuery(t *testing.T) {
	t.Parallel()

	q := &stubQuerier{cpuCount: 8}

	got := capacity.Resolve(context.Background(), q, "", 0, 0)

	assert.Equal(t, capacity.DefaultMaxInFlight, got.MaxInFlight)
	assert.Equal(t, capacity.SourceDefault, got.Source)
	assert.False(t, q.called, "empty node must prevent querier invocation")
}

// ---------------------------------------------------------------------------
// RESTQuerier — unit tests via httptest
// ---------------------------------------------------------------------------

// TestRESTQuerier_NodeStatus_ParsesResponse verifies that the REST querier
// correctly parses a mock PVE API response and returns CPUCount=12.
func TestRESTQuerier_NodeStatus_ParsesResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api2/json/nodes/pve01/status", r.URL.Path)

		payload := map[string]interface{}{
			"data": map[string]interface{}{
				"cpuinfo": map[string]interface{}{
					"cpus":  12,
					"cores": 12,
				},
				"memory": map[string]interface{}{
					"total": int64(17179869184), // 16 GiB
				},
				"loadavg": []float64{0.5, 0.8, 1.1},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()

	q := &capacity.RESTQuerier{
		Client: srv.Client(),
		Base:   srv.URL + "/api2/json",
	}

	status, err := q.NodeStatus(context.Background(), "pve01")
	require.NoError(t, err)
	require.NotNil(t, status)

	assert.Equal(t, 12, status.CPUCount)
	assert.Equal(t, int64(17179869184), status.MemoryBytes)
	assert.Equal(t, []float64{0.5, 0.8, 1.1}, status.LoadAvg)
}

// TestRESTQuerier_NodeStatus_FallsBackToCores verifies that when cpuinfo.cpus
// is 0, the querier falls back to cpuinfo.cores.
func TestRESTQuerier_NodeStatus_FallsBackToCores(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := map[string]interface{}{
			"data": map[string]interface{}{
				"cpuinfo": map[string]interface{}{
					"cpus":  0,
					"cores": 8,
				},
				"memory": map[string]interface{}{"total": int64(0)},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()

	q := &capacity.RESTQuerier{
		Client: srv.Client(),
		Base:   srv.URL + "/api2/json",
	}

	status, err := q.NodeStatus(context.Background(), "pve01")
	require.NoError(t, err)
	assert.Equal(t, 8, status.CPUCount, "must fall back to cores when cpus=0")
}

// TestRESTQuerier_NodeStatus_Non200_ReturnsError verifies that an HTTP non-200
// response results in an error.
func TestRESTQuerier_NodeStatus_Non200_ReturnsError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	q := &capacity.RESTQuerier{
		Client: srv.Client(),
		Base:   srv.URL + "/api2/json",
	}

	_, err := q.NodeStatus(context.Background(), "pve01")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "403")
}

// TestRESTQuerier_NodeStatus_InvalidJSON_ReturnsError verifies that malformed
// JSON in the response body produces an error.
func TestRESTQuerier_NodeStatus_InvalidJSON_ReturnsError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{broken json"))
	}))
	defer srv.Close()

	q := &capacity.RESTQuerier{
		Client: srv.Client(),
		Base:   srv.URL + "/api2/json",
	}

	_, err := q.NodeStatus(context.Background(), "pve01")
	require.Error(t, err)
}

// TestRESTQuerier_NodeStatus_ZeroCPU_ReturnsError verifies that a parsed
// response with both cpus and cores equal 0 returns an error.
func TestRESTQuerier_NodeStatus_ZeroCPU_ReturnsError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		payload := map[string]interface{}{
			"data": map[string]interface{}{
				"cpuinfo": map[string]interface{}{"cpus": 0, "cores": 0},
				"memory":  map[string]interface{}{"total": int64(0)},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()

	q := &capacity.RESTQuerier{
		Client: srv.Client(),
		Base:   srv.URL + "/api2/json",
	}

	_, err := q.NodeStatus(context.Background(), "pve01")
	require.Error(t, err, "zero CPU count must be an error")
	assert.Contains(t, err.Error(), "zero CPU count")
}

// TestRESTQuerier_NodeStatus_EmptyNode_ReturnsError verifies that an empty
// node name is rejected before any HTTP call is made.
func TestRESTQuerier_NodeStatus_EmptyNode_ReturnsError(t *testing.T) {
	t.Parallel()

	q := &capacity.RESTQuerier{Base: "https://pve:8006/api2/json"}

	_, err := q.NodeStatus(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "node name must not be empty")
}

// TestRESTQuerier_NodeStatus_SetsAuthHeader verifies that the Auth field is
// forwarded as the Authorization header.
func TestRESTQuerier_NodeStatus_SetsAuthHeader(t *testing.T) {
	t.Parallel()

	var capturedAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		payload := map[string]interface{}{
			"data": map[string]interface{}{
				"cpuinfo": map[string]interface{}{"cpus": 4, "cores": 4},
				"memory":  map[string]interface{}{"total": int64(0)},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()

	q := &capacity.RESTQuerier{
		Client: srv.Client(),
		Base:   srv.URL + "/api2/json",
		Auth:   "PVEAPIToken=root@pam!tok=secret",
	}

	_, err := q.NodeStatus(context.Background(), "pve01")
	require.NoError(t, err)
	assert.Equal(t, "PVEAPIToken=root@pam!tok=secret", capturedAuth)
}

// ---------------------------------------------------------------------------
// Stub helpers
// ---------------------------------------------------------------------------

// stubQuerier is a test-only Querier that returns controlled results.
type stubQuerier struct {
	cpuCount int
	err      error
	called   bool
}

func (s *stubQuerier) NodeStatus(_ context.Context, _ string) (*capacity.NodeStatus, error) {
	s.called = true
	if s.err != nil {
		return nil, s.err
	}
	return &capacity.NodeStatus{CPUCount: s.cpuCount}, nil
}
