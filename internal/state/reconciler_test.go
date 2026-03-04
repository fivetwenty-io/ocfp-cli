package state_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocfp/ocfp-cli-go/internal/cpi"
	"github.com/ocfp/ocfp-cli-go/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockProvider implements cpi.Provider for testing.
type mockProvider struct {
	name   string
	region string
}

func (m *mockProvider) Name() string                                               { return m.name }
func (m *mockProvider) Region() string                                             { return m.region }
func (m *mockProvider) Authenticate(_ctx context.Context) error                    { return nil }
func (m *mockProvider) ValidateCredentials(_ctx context.Context) error             { return nil }
func (m *mockProvider) NetworkManager() cpi.NetworkManager                         { return nil }
func (m *mockProvider) ComputeManager() cpi.ComputeManager                         { return nil }
func (m *mockProvider) StorageManager() cpi.StorageManager                         { return nil }
func (m *mockProvider) SecurityManager() cpi.SecurityManager                       { return nil }
func (m *mockProvider) LoadBalancerManager() cpi.LoadBalancerManager               { return nil }
func (m *mockProvider) Network() cpi.NetworkManager                                { return nil }
func (m *mockProvider) Compute() cpi.ComputeManager                                { return nil }
func (m *mockProvider) Storage() cpi.StorageManager                                { return nil }
func (m *mockProvider) Security() cpi.SecurityManager                              { return nil }
func (m *mockProvider) LoadBalancer() cpi.LoadBalancerManager                      { return nil }
func (m *mockProvider) SupportsStorage() bool                                      { return true }
func (m *mockProvider) Initialize(_ctx context.Context, _config interface{}) error { return nil }
func (m *mockProvider) Cleanup(_ctx context.Context) error                         { return nil }

func TestNewReconciler(t *testing.T) {
	tests := []struct {
		name        string
		provider    cpi.Provider
		manager     *state.Manager
		blocName    string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid reconciler creation",
			provider:    &mockProvider{name: "test", region: "us-east-1"},
			manager:     mustCreateTestManager(t),
			blocName:    "test-bloc",
			expectError: false,
		},
		{
			name:        "nil provider",
			provider:    nil,
			manager:     mustCreateTestManager(t),
			blocName:    "test-bloc",
			expectError: true,
			errorMsg:    "provider cannot be nil",
		},
		{
			name:        "nil state manager",
			provider:    &mockProvider{name: "test", region: "us-east-1"},
			manager:     nil,
			blocName:    "test-bloc",
			expectError: true,
			errorMsg:    "state manager cannot be nil",
		},
		{
			name:        "empty bloc name",
			provider:    &mockProvider{name: "test", region: "us-east-1"},
			manager:     mustCreateTestManager(t),
			blocName:    "",
			expectError: true,
			errorMsg:    "bloc name cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler, err := state.NewReconciler(tt.provider, tt.manager, tt.blocName)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, reconciler)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, reconciler)
			}
		})
	}
}

func TestReconcile_BasicFlow(t *testing.T) {
	// Setup
	provider := &mockProvider{name: "test-provider", region: "test-region"}
	manager := mustCreateTestManager(t)
	blocName := "test-bloc"

	reconciler, err := state.NewReconciler(provider, manager, blocName)
	require.NoError(t, err)

	ctx := context.Background()

	// Test dry-run mode
	t.Run("dry run mode", func(t *testing.T) {
		opts := state.ReconcileOptions{
			DryRun:   true,
			Strategy: state.MergeStrategyAddOnly,
		}

		result, err := reconciler.Reconcile(ctx, opts)
		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, 0, result.ResourcesAdded)
		assert.Equal(t, 0, result.ResourcesUpdated)
		assert.Equal(t, 0, result.ResourcesRemoved)
		assert.Greater(t, result.Duration.Nanoseconds(), int64(0))
	})

	// Test different merge strategies
	t.Run("add-only strategy", func(t *testing.T) {
		opts := state.ReconcileOptions{
			DryRun:   true,
			Strategy: state.MergeStrategyAddOnly,
		}

		result, err := reconciler.Reconcile(ctx, opts)
		require.NoError(t, err)
		assert.NotNil(t, result)
	})

	t.Run("update strategy", func(t *testing.T) {
		opts := state.ReconcileOptions{
			DryRun:   true,
			Strategy: state.MergeStrategyUpdate,
		}

		result, err := reconciler.Reconcile(ctx, opts)
		require.NoError(t, err)
		assert.NotNil(t, result)
	})

	t.Run("full strategy", func(t *testing.T) {
		opts := state.ReconcileOptions{
			DryRun:   true,
			Strategy: state.MergeStrategyFull,
		}

		result, err := reconciler.Reconcile(ctx, opts)
		require.NoError(t, err)
		assert.NotNil(t, result)
	})
}

func TestValidateProvider(t *testing.T) {
	provider := &mockProvider{name: "test", region: "test-region"}
	manager := mustCreateTestManager(t)
	blocName := "test-bloc"

	reconciler, err := state.NewReconciler(provider, manager, blocName)
	require.NoError(t, err)

	ctx := context.Background()
	err = reconciler.ValidateProvider(ctx)
	assert.NoError(t, err)
}

func TestMergeStrategy_String(t *testing.T) {
	tests := []struct {
		strategy state.MergeStrategy
		expected string
	}{
		{state.MergeStrategyAddOnly, "add-only"},
		{state.MergeStrategyUpdate, "update"},
		{state.MergeStrategyFull, "full"},
		{state.MergeStrategy(999), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.strategy.String())
		})
	}
}

func TestParseMergeStrategy(t *testing.T) {
	tests := []struct {
		input       string
		expected    state.MergeStrategy
		expectError bool
	}{
		{"add-only", state.MergeStrategyAddOnly, false},
		{"update", state.MergeStrategyUpdate, false},
		{"full", state.MergeStrategyFull, false},
		{"invalid", state.MergeStrategyAddOnly, true},
		{"", state.MergeStrategyAddOnly, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			strategy, err := state.ParseMergeStrategy(tt.input)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "invalid merge strategy")
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expected, strategy)
			}
		})
	}
}

// mustCreateTestManager creates a test state manager in a temp directory.
func mustCreateTestManager(t *testing.T) *state.Manager {
	t.Helper()

	tmpDir := filepath.Join(os.TempDir(), "ocfp-test-state")
	t.Cleanup(func() {
		_ = os.RemoveAll(tmpDir)
	})

	manager, err := state.NewManager(tmpDir)
	require.NoError(t, err)

	return manager
}
